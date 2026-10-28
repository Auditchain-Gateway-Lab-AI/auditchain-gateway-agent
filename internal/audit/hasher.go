package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"

	"auditchain-agent/internal/canonicalstate"
)

// CompareState compares live client row data with the expected state in the offchain log.
// It returns clientHash, offchainHash, isMatch, and discrepancies (if any).
func CompareState(clientID, resource, action string, clientData map[string]interface{}, clientFound bool, offchainMetadata string) (clientHash string, offchainHash string, isMatch bool, discrepancies []Discrepancy, err error) {
	// Handle DELETE action
	if strings.EqualFold(strings.TrimSpace(action), "DELETE") {
		expected := map[string]interface{}{}
		offchainHash, _, err = canonicalstate.HashState(clientID, resource, expected)
		if err != nil {
			return "", "", false, nil, fmt.Errorf("hash offchain delete state: %w", err)
		}

		if !clientFound {
			clientHash, _, _ = canonicalstate.HashState(clientID, resource, expected)
			return clientHash, offchainHash, true, nil, nil
		}

		clientHash, _, _ = canonicalstate.HashState(clientID, resource, clientData)
		discrepancies = append(discrepancies, Discrepancy{
			Field:    "row_existence",
			Expected: "row deleted (absent)",
			Actual:   "row still exists",
		})
		return clientHash, offchainHash, false, discrepancies, nil
	}

	// For INSERT / UPDATE: row must exist in client database
	if !clientFound {
		expectedState, _ := ExtractOffchainState(offchainMetadata)
		offchainHash, _, _ = canonicalstate.HashState(clientID, resource, expectedState)
		clientHash, _, _ = canonicalstate.HashState(clientID, resource, map[string]interface{}{})
		discrepancies = append(discrepancies, Discrepancy{
			Field:    "row_existence",
			Expected: "row exists",
			Actual:   "row not found",
		})
		return clientHash, offchainHash, false, discrepancies, nil
	}

	// Extract expected offchain state from metadata JSON
	expectedState, err := ExtractOffchainState(offchainMetadata)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("extract offchain state: %w", err)
	}

	// Canonicalize both states
	offchainHash, expectedCanonicalBytes, err := canonicalstate.HashState(clientID, resource, expectedState)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("hash offchain state: %w", err)
	}

	// Project client data to matching keys in expected state
	var expectedCanonicalMap map[string]interface{}
	if err := json.Unmarshal(expectedCanonicalBytes, &expectedCanonicalMap); err != nil {
		expectedCanonicalMap = expectedState
	}

	clientProjected := make(map[string]interface{}, len(expectedCanonicalMap))
	clientLowerKeys := make(map[string]interface{}, len(clientData))
	for k, v := range clientData {
		clientLowerKeys[strings.ToLower(strings.TrimSpace(k))] = v
	}

	for k, expectedVal := range expectedCanonicalMap {
		normalizedKey := strings.ToLower(strings.TrimSpace(k))
		actualVal, exists := clientLowerKeys[normalizedKey]
		if !exists {
			discrepancies = append(discrepancies, Discrepancy{
				Field:    k,
				Expected: expectedVal,
				Actual:   nil,
			})
			continue
		}

		// Check value equality for discrepancy report
		if !areValuesEquivalent(expectedVal, actualVal) {
			clientProjected[k] = actualVal
			discrepancies = append(discrepancies, Discrepancy{
				Field:    k,
				Expected: expectedVal,
				Actual:   actualVal,
			})
		} else {
			// Values are equivalent (e.g. identical primitives, or matching timestamps with different formats).
			// Use expectedVal in clientProjected so that its representation matches expectedState exactly,
			// ensuring clientHash == offchainHash.
			clientProjected[k] = expectedVal
		}
	}

	clientHash, _, err = canonicalstate.HashState(clientID, resource, clientProjected)
	if err != nil {
		return "", "", false, nil, fmt.Errorf("hash client state: %w", err)
	}

	isMatch = clientHash == offchainHash && len(discrepancies) == 0
	return clientHash, offchainHash, isMatch, discrepancies, nil
}

// ExtractOffchainState decodes the metadata field of an AuditLog.
// If metadata is wrapped in "data_baru" or "after", it extracts that object.
func ExtractOffchainState(rawMetadata string) (map[string]interface{}, error) {
	rawMetadata = strings.TrimSpace(rawMetadata)
	if rawMetadata == "" || rawMetadata == "{}" {
		return map[string]interface{}{}, nil
	}

	dec := json.NewDecoder(strings.NewReader(rawMetadata))
	dec.UseNumber()

	var state map[string]interface{}
	if err := dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	var trailing interface{}
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("trailing metadata JSON")
	}

	for _, wrapper := range []string{"data_baru", "after"} {
		for key, val := range state {
			if strings.EqualFold(key, wrapper) {
				if wrappedMap, ok := val.(map[string]interface{}); ok && wrappedMap != nil {
					return wrappedMap, nil
				}
			}
		}
	}

	return state, nil
}

func areValuesEquivalent(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// String representation comparison for numbers and primitives
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	if aStr == bStr {
		return true
	}

	// Timestamp comparison (handles Debezium epoch microseconds vs Go time.Time / RFC3339 string)
	if tA, okA := parseTimestamp(a); okA {
		if tB, okB := parseTimestamp(b); okB {
			diff := tA.Sub(tB)
			if diff < 0 {
				diff = -diff
			}
			if diff < time.Second {
				return true
			}
		}
	}

	return reflect.DeepEqual(a, b)
}

func parseTimestamp(v any) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}

	switch val := v.(type) {
	case time.Time:
		return val.UTC(), true
	case json.Number:
		if n, err := val.Int64(); err == nil {
			return parseEpoch(n)
		}
		if f, err := val.Float64(); err == nil {
			return parseEpoch(int64(f))
		}
	case int64:
		return parseEpoch(val)
	case int:
		return parseEpoch(int64(val))
	case int32:
		return parseEpoch(int64(val))
	case uint64:
		return parseEpoch(int64(val))
	case float64:
		return parseEpoch(int64(val))
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.999999999Z07:00",
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC(), true
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return parseEpoch(n)
		}
	}

	return time.Time{}, false
}

func parseEpoch(n int64) (time.Time, bool) {
	if n > 100_000_000_000_000 { // Microseconds (> 1e14, e.g. Debezium io.debezium.time.MicroTimestamp)
		return time.UnixMicro(n).UTC(), true
	} else if n > 100_000_000_000 { // Milliseconds (> 1e11)
		return time.UnixMilli(n).UTC(), true
	} else if n > 100_000_000 { // Seconds (> 1e8)
		return time.Unix(n, 0).UTC(), true
	}
	return time.Time{}, false
}
