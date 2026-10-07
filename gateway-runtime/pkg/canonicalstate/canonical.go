// Package canonicalstate provides the deterministic representation used when
// comparing a client row with a trusted AuditChain reference. It is kept
// separate from the audit-leaf hasher: an audit leaf describes an event,
// while a state hash describes the resulting client row.
package canonicalstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"go-blockchain-api/pkg/crypto"
)

const SchemaVersion = 1

var (
	ErrInvalidJSON       = errors.New("canonical_state_invalid_json")
	ErrExpectedObject    = errors.New("canonical_state_expected_object")
	ErrDuplicateKey      = errors.New("canonical_state_duplicate_key")
	ErrUnsupportedNumber = errors.New("canonical_state_invalid_number")
)

// CanonicalizeJSON parses an object/array/scalar and returns deterministic JSON
// bytes. Object keys are trimmed and lower-cased recursively. A key collision
// after normalization is rejected instead of silently overwriting a value.
// JSON numbers are emitted in a stable decimal form so 1, 1.0, and 1e0 have
// the same state representation.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, ErrInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidJSON, err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, ErrInvalidJSON
	}

	normalized, err := normalizeValue(value)
	if err != nil {
		return nil, err
	}
	encoded, err := marshalCanonical(normalized)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// CanonicalizeObject is the object-only variant used for table state. It also
// returns the normalized map for field-level comparison.
func CanonicalizeObject(raw []byte) (map[string]interface{}, []byte, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return nil, nil, err
	}
	var object map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, nil, ErrExpectedObject
	}
	return object, canonical, nil
}

// HashJSON computes the versioned canonical state hash for one client
// resource. The resource and tenant are included so the same JSON cannot be
// replayed against another tenant or table.
func HashJSON(clientID, resource string, raw []byte) (string, []byte, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return "", nil, err
	}
	return HashCanonical(clientID, resource, canonical), canonical, nil
}

func HashCanonical(clientID, resource string, canonical []byte) string {
	preimage := fmt.Sprintf("%d|%s|%s|%s", SchemaVersion, clientID, resource, canonical)
	return crypto.GenerateSHA3_256(preimage)
}

func normalizeValue(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			normalizedKey := normalizeKey(key)
			if normalizedKey == "" {
				return nil, fmt.Errorf("%w: empty key", ErrDuplicateKey)
			}
			if _, exists := result[normalizedKey]; exists {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateKey, normalizedKey)
			}
			normalizedChild, err := normalizeValue(child)
			if err != nil {
				return nil, err
			}
			result[normalizedKey] = normalizedChild
		}
		return result, nil
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i, child := range typed {
			normalizedChild, err := normalizeValue(child)
			if err != nil {
				return nil, err
			}
			result[i] = normalizedChild
		}
		return result, nil
	case json.Number:
		canonical, err := canonicalNumber(typed.String())
		if err != nil {
			return nil, err
		}
		return json.Number(canonical), nil
	default:
		return value, nil
	}
}

func normalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func marshalCanonical(value interface{}) ([]byte, error) {
	// encoding/json sorts string map keys, and json.Number is emitted as a
	// number after canonicalNumber has validated its grammar.
	return json.Marshal(value)
}

// canonicalNumber normalizes the finite decimal grammar accepted by JSON
// without converting through float64 (which could corrupt large IDs).
func canonicalNumber(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrUnsupportedNumber
	}

	sign := ""
	if raw[0] == '-' {
		sign = "-"
		raw = raw[1:]
	}
	if raw == "" {
		return "", ErrUnsupportedNumber
	}

	exponent := 0
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		parsed := raw[index+1:]
		raw = raw[:index]
		if parsed == "" {
			return "", ErrUnsupportedNumber
		}
		value := 0
		exponentSign := 1
		if parsed[0] == '+' || parsed[0] == '-' {
			if parsed[0] == '-' {
				exponentSign = -1
			}
			parsed = parsed[1:]
		}
		if parsed == "" {
			return "", ErrUnsupportedNumber
		}
		for _, char := range parsed {
			if char < '0' || char > '9' {
				return "", ErrUnsupportedNumber
			}
			value = value*10 + int(char-'0')
			if value > 1_000_000 {
				return "", ErrUnsupportedNumber
			}
		}
		exponent = exponentSign * value
	}

	parts := strings.Split(raw, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return "", ErrUnsupportedNumber
	}
	integerPart := parts[0]
	fractionPart := ""
	if len(parts) == 2 {
		fractionPart = parts[1]
	}
	for _, char := range integerPart + fractionPart {
		if char < '0' || char > '9' {
			return "", ErrUnsupportedNumber
		}
	}

	digits := integerPart + fractionPart
	leadingZeroes := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	decimalPosition := len(integerPart) + exponent - leadingZeroes
	if decimalPosition <= 0 {
		digits = strings.Repeat("0", -decimalPosition) + digits
		decimalPosition = 0
	}
	if decimalPosition >= len(digits) {
		digits += strings.Repeat("0", decimalPosition-len(digits))
		decimalPosition = len(digits)
	}

	result := digits[:decimalPosition]
	if result == "" {
		result = "0"
	}
	if decimalPosition < len(digits) {
		result += "." + digits[decimalPosition:]
		result = strings.TrimRight(result, "0")
		result = strings.TrimRight(result, ".")
	}
	result = strings.TrimLeft(result, "0")
	if result == "" || result[0] == '.' {
		result = "0" + result
	}
	if result == "0" {
		return result, nil
	}
	if sign == "-" {
		return sign + result, nil
	}
	return result, nil
}

// IsNormalizedKey reports whether a key is safe for the canonical state
// namespace. It is useful to Agent clients that validate individual fields.
func IsNormalizedKey(key string) bool {
	return key != "" && key == normalizeKey(key) && !strings.ContainsFunc(key, unicode.IsSpace)
}

// SortedKeys returns normalized object keys in deterministic order for callers
// that need to build a parameterized update statement.
func SortedKeys(object map[string]interface{}) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
