package audit

import (
	"testing"
	"time"
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

func TestCompareState_TimestampNormalization(t *testing.T) {
	clientID := "67e493e9-f7a1-441a-8229-f688cb876fd2"
	resource := "RUANGAN:1516"
	action := "UPDATE"

	// Debezium metadata with epoch microseconds
	metadata := `{"ID": 1516, "NAMA": "PW-RUANGAN", "CREATED_AT": 1792732263000000, "UPDATED_AT": 1793082776000000}`

	// Client database returns time.Time
	createdAt, _ := time.Parse(time.RFC3339, "2026-10-23T05:11:03Z")
	updatedAt, _ := time.Parse(time.RFC3339, "2026-10-27T06:32:56Z")
	clientData := map[string]interface{}{
		"ID":         1516,
		"NAMA":       "PW-RUANGAN",
		"CREATED_AT": createdAt,
		"UPDATED_AT": updatedAt,
	}

	clientHash, offchainHash, isMatch, discrepancies, err := CompareState(
		clientID, resource, action, clientData, true, metadata,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isMatch {
		t.Fatalf("expected match with timestamp normalization, got discrepancies: %+v", discrepancies)
	}
	if clientHash != offchainHash {
		t.Fatalf("hashes should match: client=%s, offchain=%s", clientHash, offchainHash)
	}
	if len(discrepancies) != 0 {
		t.Fatalf("expected 0 discrepancies, got: %+v", discrepancies)
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
