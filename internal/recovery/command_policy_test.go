package recovery

import (
	"os"
	"path/filepath"
	"testing"

	"auditchain-agent/internal/verify"
)

func recoveryReadMapping(table string) (verify.ReadProjection, bool) {
	if table != "RUANGAN" {
		return verify.ReadProjection{}, false
	}
	return verify.ReadProjection{Schema: "POLINEMA", Table: "RUANGAN", PrimaryKey: "ID", StateColumns: []string{"ID", "NAMA"}}, true
}

func TestValidateDesiredStateRejectsRedactionMarkers(t *testing.T) {
	table := CommandTablePolicy{PrimaryKeyField: "id", Columns: map[string]CommandColumnPolicy{"id": {State: true}, "nama": {State: true}}}
	for _, marker := range []string{"[REDACTED]", "***REDACTED***"} {
		failure := validateDesiredState(table, "7", map[string]interface{}{"id": 7, "nama": marker})
		if failure == nil || failure.Code != "desired_state_redacted" {
			t.Fatalf("marker %q failure = %#v", marker, failure)
		}
	}
}

func TestRuanganPilotPolicyMatchesCommittedReadProjection(t *testing.T) {
	read, err := verify.LoadReadPolicy(filepath.Join("..", "..", "verify-policy.yml"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := LoadCommandPolicy(filepath.Join("..", "..", "recovery-policy.ruangan.example.yml"), read.ReadMapping)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := policy.tables["RUANGAN"]; !ok {
		t.Fatal("RUANGAN pilot table missing")
	}
}

func TestLoadCommandPolicyRequiresExactReadProjectionAndAllowlist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-policy.yml")
	validPolicy := `version: 1
tables:
  - resource_table: RUANGAN
    schema: POLINEMA
    table: RUANGAN
    primary_key_field: id
    primary_key_column: ID
    operations: [UPSERT, DELETE]
    columns:
      id:
        database_column: ID
        state: true
        insert: true
        immutable: true
      nama:
        database_column: NAMA
        state: true
        insert: true
        update: true
`
	if err := os.WriteFile(path, []byte(validPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := LoadCommandPolicy(path, recoveryReadMapping)
	if err != nil {
		t.Fatalf("LoadCommandPolicy() error = %v", err)
	}
	if table, ok := policy.table("ruangan"); !ok || !table.allows("UPSERT") || !table.allows("DELETE") {
		t.Fatalf("policy did not load exact table operations: %#v", table)
	}

	invalidProjection := func(string) (verify.ReadProjection, bool) {
		return verify.ReadProjection{Schema: "OTHER", Table: "RUANGAN", PrimaryKey: "ID", StateColumns: []string{"ID", "NAMA"}}, true
	}
	if _, err := LoadCommandPolicy(path, invalidProjection); err == nil {
		t.Fatal("policy must fail when it diverges from the read projection")
	}
}

func TestLoadCommandPolicyFailsClosedForEmptyOrSensitiveMappings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-policy.yml")
	if err := os.WriteFile(path, []byte("version: 1\ntables: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCommandPolicy(path, recoveryReadMapping); err == nil {
		t.Fatal("empty recovery policy must not enable writes")
	}

	sensitive := `version: 1
tables:
  - resource_table: RUANGAN
    schema: POLINEMA
    table: RUANGAN
    primary_key_field: id
    primary_key_column: ID
    operations: [UPSERT]
    columns:
      id: {database_column: ID, state: true, insert: true, immutable: true}
      nama: {database_column: NAMA, state: true, insert: true, update: true}
      token: {database_column: API_TOKEN, state: true, insert: true}
`
	if err := os.WriteFile(path, []byte(sensitive), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCommandPolicy(path, recoveryReadMapping); err == nil {
		t.Fatal("sensitive columns must not be allowed")
	}
}

func TestAssessOperationChecksAllowlistPayloadAndChangedColumns(t *testing.T) {
	policy := &CommandPolicySet{tables: map[string]CommandTablePolicy{
		"RUANGAN": {
			ResourceTable: "RUANGAN", PrimaryKeyField: "id", Operations: []string{"UPSERT"},
			Columns: map[string]CommandColumnPolicy{
				"id":         {DatabaseColumn: "ID", State: true, Insert: true, Immutable: true},
				"nama":       {DatabaseColumn: "NAMA", State: true, Insert: true, Update: true},
				"created_at": {DatabaseColumn: "CREATED_AT", State: true, Insert: true, Immutable: true},
			},
		},
	}}
	allFields := []string{"ID", "NAMA", "CREATED_AT"}
	if allowed, reason := policy.assessOperation("ruangan", "UPSERT", true, allFields, []string{"NAMA"}); !allowed || reason != "" {
		t.Fatalf("allowed update assessment = %v/%q", allowed, reason)
	}
	if allowed, reason := policy.assessOperation("RUANGAN", "UPSERT", true, allFields, []string{"CREATED_AT"}); allowed || reason != "column_not_updateable" {
		t.Fatalf("immutable update assessment = %v/%q, want column_not_updateable", allowed, reason)
	}
	if allowed, reason := policy.assessOperation("RUANGAN", "UPSERT", true, []string{"ID", "NAMA"}, []string{"NAMA"}); allowed || reason != "payload_incomplete" {
		t.Fatalf("incomplete state assessment = %v/%q, want payload_incomplete", allowed, reason)
	}
	if allowed, reason := policy.assessOperation("RUANGAN", "DELETE", true, nil, nil); allowed || reason != "operation_not_allowed" {
		t.Fatalf("disallowed operation assessment = %v/%q, want operation_not_allowed", allowed, reason)
	}
	if allowed, reason := policy.assessOperation("RUANGAN", "UPSERT", false, allFields, nil); !allowed || reason != "" {
		t.Fatalf("allowed insert assessment = %v/%q", allowed, reason)
	}
}

func TestCommandFingerprintIsStableAndIncludesDesiredState(t *testing.T) {
	request := RecoveryCommandRequest{RequestID: "req", IdempotencyKey: "key", Operation: "UPSERT", DesiredState: map[string]interface{}{"nama": "Ruang", "id": 1}}
	first, err := commandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	request.DesiredState = map[string]interface{}{"ID": 1.0, " NAMA ": "Ruang"}
	second, err := commandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("equivalent canonical JSON should have a stable semantic fingerprint")
	}
	request.DesiredState = map[string]interface{}{"id": 1, "nama": "Different"}
	third, err := commandFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("different desired state must produce a different semantic fingerprint")
	}
}

func TestDisabledCommandServiceRejectsWrites(t *testing.T) {
	service := &CommandService{enabled: false}
	_, failure := service.Execute(nil, "RUANGAN", "1019", RecoveryCommandRequest{})
	if failure == nil || failure.Status != 403 || failure.Code != "recovery_disabled" {
		t.Fatalf("failure = %#v, want recovery_disabled", failure)
	}
}
