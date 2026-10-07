// Package redaction contains the single field-redaction policy used by
// recovery previews and recovery evidence exposed outside the write path.
// It deliberately does not participate in canonical state hashing: the Agent
// must receive the exact desired state, while UI/evidence payloads must not
// expose credentials or bearer material.
package redaction

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const Mask = "***REDACTED***"

var sensitiveFragments = []string{
	"password",
	"passwd",
	"pwd",
	"token",
	"secret",
	"api_key",
	"apikey",
	"private_key",
	"privatekey",
	"authorization",
	"cookie",
	"credential",
	"passphrase",
	"pin",
}

// JSON parses a JSON payload and returns a compact, recursively redacted JSON
// value. It rejects trailing JSON so callers do not accidentally expose a
// partially parsed payload.
func JSON(raw []byte) ([]byte, error) {
	value, err := Value(raw)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal redacted JSON: %w", err)
	}
	return encoded, nil
}

// Value returns a recursively redacted representation suitable for a JSON
// response. Numeric values remain json.Number so redaction never changes
// their textual meaning.
func Value(raw []byte) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode JSON for redaction: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("trailing JSON is not allowed")
		}
		return nil, fmt.Errorf("trailing JSON is not valid: %w", err)
	}
	return redactValue(value), nil
}

func redactValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			if isSensitiveKey(key) {
				out[key] = Mask
				continue
			}
			out[key] = redactValue(item)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(typed))
		for i, item := range typed {
			out[i] = redactValue(item)
		}
		return out
	default:
		return value
	}
}

func isSensitiveKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")
	for _, fragment := range sensitiveFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
