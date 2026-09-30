// Package canonicalstate implements the versioned state representation used
// by the Gateway and Agent when comparing a client row with an audited state.
package canonicalstate

import (
	"bytes"
	"crypto/sha3"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const SchemaVersion = 1

var (
	ErrTrailingJSON           = errors.New("trailing JSON data")
	ErrNormalizedKeyCollision = errors.New("object key collision after normalization")
)

// CanonicalizeJSON parses one JSON value without converting numbers through
// float64 and emits deterministic JSON. Object keys are trimmed and lowercased
// recursively; arrays preserve their order.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode canonical JSON: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, ErrTrailingJSON
		}
		return nil, fmt.Errorf("decode trailing JSON: %w", err)
	}

	normalized, err := normalize(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}

// CanonicalizeValue converts values returned by database/sql into the JSON
// types accepted by CanonicalizeJSON. In particular, driver byte slices are
// treated as text and timestamps use UTC RFC3339Nano.
func CanonicalizeValue(value any) ([]byte, error) {
	converted, err := databaseValue(value)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(converted)
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	return CanonicalizeJSON(raw)
}

// CanonicalizeObject canonicalizes a state object and rejects non-object JSON.
func CanonicalizeObject(state map[string]any) ([]byte, error) {
	if state == nil {
		state = map[string]any{}
	}
	return CanonicalizeValue(state)
}

// Hash computes SHA3-256 over the versioned resource preimage.
func Hash(clientID, resource string, canonicalJSON []byte) string {
	preimage := fmt.Sprintf("%d|%s|%s|%s", SchemaVersion, clientID, resource, canonicalJSON)
	sum := sha3.Sum256([]byte(preimage))
	return fmt.Sprintf("%x", sum[:])
}

// HashState canonicalizes state and returns both the canonical bytes and hash.
func HashState(clientID, resource string, state map[string]any) (string, []byte, error) {
	canonical, err := CanonicalizeObject(state)
	if err != nil {
		return "", nil, err
	}
	return Hash(clientID, resource, canonical), canonical, nil
}

// HashCanonical hashes already canonical JSON after checking that it is a
// single JSON value. It is used for values stored in the idempotency record.
func HashCanonical(clientID, resource string, canonical []byte) (string, error) {
	checked, err := CanonicalizeJSON(canonical)
	if err != nil {
		return "", err
	}
	return Hash(clientID, resource, checked), nil
}

func normalize(value any) (any, error) {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "" {
				return nil, fmt.Errorf("empty object key")
			}
			if _, exists := out[normalizedKey]; exists {
				return nil, fmt.Errorf("%w: %q", ErrNormalizedKeyCollision, normalizedKey)
			}
			normalizedChild, err := normalize(child)
			if err != nil {
				return nil, err
			}
			out[normalizedKey] = normalizedChild
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			normalizedChild, err := normalize(child)
			if err != nil {
				return nil, err
			}
			out[i] = normalizedChild
		}
		return out, nil
	case json.Number:
		canonical, err := canonicalNumber(v.String())
		if err != nil {
			return nil, err
		}
		return json.Number(canonical), nil
	default:
		return value, nil
	}
}

func databaseValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	switch v := value.(type) {
	case time.Time:
		return v.UTC().Format(time.RFC3339Nano), nil
	case []byte:
		return string(v), nil
	case json.Number:
		return v, nil
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return value, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			converted, err := databaseValue(child)
			if err != nil {
				return nil, err
			}
			out[key] = converted
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			converted, err := databaseValue(child)
			if err != nil {
				return nil, err
			}
			out[i] = converted
		}
		return out, nil
	}

	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil, nil
	}
	return nil, fmt.Errorf("unsupported database value type %T", value)
}

func canonicalNumber(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("empty JSON number")
	}

	negative := false
	if input[0] == '-' {
		negative = true
		input = input[1:]
	}
	if input == "" {
		return "", fmt.Errorf("invalid JSON number")
	}

	exponent := 0
	if index := strings.IndexAny(input, "eE"); index >= 0 {
		exponentValue, err := strconv.Atoi(input[index+1:])
		if err != nil || exponentValue < -10000 || exponentValue > 10000 {
			return "", fmt.Errorf("JSON number exponent out of range")
		}
		exponent = exponentValue
		input = input[:index]
	}

	decimalDigits := 0
	if index := strings.IndexByte(input, '.'); index >= 0 {
		decimalDigits = len(input) - index - 1
		input = input[:index] + input[index+1:]
	}
	if input == "" {
		return "", fmt.Errorf("invalid JSON number")
	}
	for _, r := range input {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("invalid JSON number")
		}
	}

	// Remove leading zeroes without changing the all-zero value.
	input = strings.TrimLeft(input, "0")
	if input == "" {
		return "0", nil
	}
	// Strip trailing fractional zeroes before applying the exponent.
	for decimalDigits > 0 && strings.HasSuffix(input, "0") {
		input = strings.TrimSuffix(input, "0")
		decimalDigits--
	}
	scale := decimalDigits - exponent

	var result string
	switch {
	case scale <= 0:
		result = input + strings.Repeat("0", -scale)
	case scale >= len(input):
		result = "0." + strings.Repeat("0", scale-len(input)) + input
	default:
		result = input[:len(input)-scale] + "." + input[len(input)-scale:]
	}
	if strings.Contains(result, ".") {
		result = strings.TrimRight(strings.TrimRight(result, "0"), ".")
	}
	if result == "0" || strings.HasPrefix(result, "0.") {
		negative = false
	}
	if negative {
		result = "-" + result
	}
	return result, nil
}
