package audit

import (
	"testing"
)

func TestExtractOffchainState(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected map[string]interface{}
	}{
		{
			name:     "wrapped in data_baru",
			raw:      `{"data_baru":{"nama":"KABUPATEN BOGOR","kode":"3201"}}`,
			expected: map[string]interface{}{"nama": "KABUPATEN BOGOR", "kode": "3201"},
		},
		{
			name:     "wrapped in after",
			raw:      `{"after":{"nama":"KABUPATEN BANDUNG","kode":"3204"}}`,
			expected: map[string]interface{}{"nama": "KABUPATEN BANDUNG", "kode": "3204"},
		},
		{
			name:     "direct map",
			raw:      `{"nama":"KABUPATEN BEKASI","kode":"3216"}`,
			expected: map[string]interface{}{"nama": "KABUPATEN BEKASI", "kode": "3216"},
		},
		{
			name:     "empty json",
			raw:      `{}`,
			expected: map[string]interface{}{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := ExtractOffchainState(tt.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(res) != len(tt.expected) {
				t.Errorf("expected %d keys, got %d", len(tt.expected), len(res))
			}
		})
	}
}

func TestCompareState_Match(t *testing.T) {
	clientID := "client-satu-peta"
	resource := "KABUPATEN:1"
	action := "UPDATE"

	metadata := `{"data_baru":{"nama":"KABUPATEN BOGOR","status":"AKTIF"}}`
	clientData := map[string]interface{}{
		"NAMA":   "KABUPATEN BOGOR",
		"STATUS": "AKTIF",
	}

	clientHash, offchainHash, isMatch, discrepancies, err := CompareState(
		clientID, resource, action, clientData, true, metadata,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isMatch {
		t.Fatalf("expected match, got mismatch with discrepancies: %+v", discrepancies)
	}
	if clientHash != offchainHash {
		t.Fatalf("hashes should match: client=%s, offchain=%s", clientHash, offchainHash)
	}
}

func TestCompareState_Tampered(t *testing.T) {
	clientID := "client-satu-peta"
	resource := "KABUPATEN:1"
	action := "UPDATE"

	metadata := `{"data_baru":{"nama":"KABUPATEN BOGOR","status":"AKTIF"}}`
	clientData := map[string]interface{}{
		"NAMA":   "KABUPATEN BOGOR (TAMPERED)",
		"STATUS": "AKTIF",
	}

	clientHash, offchainHash, isMatch, discrepancies, err := CompareState(
		clientID, resource, action, clientData, true, metadata,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isMatch {
		t.Fatalf("expected mismatch due to tampering, but got match")
	}
	if clientHash == offchainHash {
		t.Fatalf("hashes should differ on tampered state: %s", clientHash)
	}
	if len(discrepancies) == 0 {
		t.Fatalf("expected discrepancies to be recorded")
	}
}

func TestCompareState_Delete(t *testing.T) {
	clientID := "client-satu-peta"
	resource := "KABUPATEN:1"
	action := "DELETE"

	// When row is not found in DB -> Match!
	_, _, isMatch, _, err := CompareState(clientID, resource, action, nil, false, "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isMatch {
		t.Fatalf("expected match for absent row on DELETE")
	}

	// When row is still found in DB -> Tampered!
	_, _, isMatch, _, err = CompareState(clientID, resource, action, map[string]interface{}{"id": 1}, true, "{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if isMatch {
		t.Fatalf("expected mismatch when row still exists on DELETE")
	}
}
