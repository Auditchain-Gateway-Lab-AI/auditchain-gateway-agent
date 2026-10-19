package verify

import (
	"os"
	"path/filepath"
	"testing"
)

func writeReadPolicyForTest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "verify-policy.yml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write policy fixture: %v", err)
	}
	return path
}

func TestLoadReadPolicyAllowsExactReadProjection(t *testing.T) {
	policy, err := LoadReadPolicy(writeReadPolicyForTest(t, `
version: 1
tables:
  - resource_table: agama
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID, KODE, NAMA]
`))
	if err != nil {
		t.Fatalf("LoadReadPolicy() error = %v", err)
	}
	if got := policy.TableCount(); got != 1 {
		t.Fatalf("TableCount() = %d, want 1", got)
	}

	projection, ok := policy.ReadMapping(" AGAMA ")
	if !ok {
		t.Fatal("ReadMapping(AGAMA) was not allowed")
	}
	if projection.Schema != "POLINEMA" || projection.Table != "AGAMA" || projection.PrimaryKey != "ID" {
		t.Fatalf("unexpected projection: %+v", projection)
	}
	if len(projection.StateColumns) != 3 || projection.StateColumns[0] != "ID" || projection.StateColumns[1] != "KODE" || projection.StateColumns[2] != "NAMA" {
		t.Fatalf("unexpected state columns: %#v", projection.StateColumns)
	}

	projection.StateColumns[0] = "MUTATED"
	second, ok := policy.ReadMapping("agama")
	if !ok || second.StateColumns[0] != "ID" {
		t.Fatalf("ReadMapping exposed mutable policy state: %+v", second)
	}
	if _, ok := policy.ReadMapping("BENTUK_MAKANAN"); ok {
		t.Fatal("unconfigured table unexpectedly allowed")
	}
}

func TestLoadReadPolicyRejectsUnsafeOrAmbiguousMappings(t *testing.T) {
	tests := []struct {
		name   string
		tables string
	}{
		{
			name: "unknown field",
			tables: `
  - resource_table: AGAMA
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID]
    operations: [UPDATE]
`,
		},
		{
			name: "invalid identifier",
			tables: `
  - resource_table: "AGAMA; DROP TABLE USERS"
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID]
`,
		},
		{
			name: "duplicate resource",
			tables: `
  - resource_table: AGAMA
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID]
  - resource_table: agama
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID]
`,
		},
		{
			name: "duplicate columns after case normalization",
			tables: `
  - resource_table: AGAMA
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [ID, id]
`,
		},
		{
			name: "primary key omitted",
			tables: `
  - resource_table: AGAMA
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: [KODE, NAMA]
`,
		},
		{
			name: "credential column",
			tables: `
  - resource_table: USERS
    schema: POLINEMA
    table: USERS
    primary_key_column: ID
    state_columns: [ID, PASSWORD_HASH]
`,
		},
		{
			name: "empty state columns",
			tables: `
  - resource_table: AGAMA
    schema: POLINEMA
    table: AGAMA
    primary_key_column: ID
    state_columns: []
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			content := "version: 1\ntables:\n" + test.tables
			if _, err := LoadReadPolicy(writeReadPolicyForTest(t, content)); err == nil {
				t.Fatal("LoadReadPolicy() succeeded for invalid policy")
			}
		})
	}
}

func TestLoadReadPolicyEmptyPolicyFailsClosed(t *testing.T) {
	policy, err := LoadReadPolicy(writeReadPolicyForTest(t, "version: 1\ntables: []\n"))
	if err != nil {
		t.Fatalf("LoadReadPolicy() error = %v", err)
	}
	if policy.TableCount() != 0 {
		t.Fatalf("TableCount() = %d, want 0", policy.TableCount())
	}
	if _, ok := policy.ReadMapping("AGAMA"); ok {
		t.Fatal("empty policy unexpectedly allowed a table")
	}
}

func TestMorbisVerifyPoliciesIncludeConfirmedTables(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "verify-policy.yml"),
		filepath.Join("..", "..", "verify-policy.example.yml"),
	}
	wantColumns := map[string][]string{
		"AGAMA":          {"ID", "NAMA", "KODE"},
		"BENTUK_MAKANAN": {"ID", "NAMA", "STATUS"},
	}

	for _, path := range paths {
		policy, err := LoadReadPolicy(path)
		if err != nil {
			t.Fatalf("LoadReadPolicy(%q) error = %v", path, err)
		}
		for resource, columns := range wantColumns {
			projection, ok := policy.ReadMapping(resource)
			if !ok {
				t.Errorf("%s: table %s is not allowlisted", path, resource)
				continue
			}
			if projection.Schema != "POLINEMA" || projection.Table != resource || projection.PrimaryKey != "ID" {
				t.Errorf("%s: unexpected %s mapping: %+v", path, resource, projection)
			}
			if len(projection.StateColumns) != len(columns) {
				t.Errorf("%s: %s columns = %#v, want %#v", path, resource, projection.StateColumns, columns)
				continue
			}
			for index := range columns {
				if projection.StateColumns[index] != columns[index] {
					t.Errorf("%s: %s columns = %#v, want %#v", path, resource, projection.StateColumns, columns)
					break
				}
			}
		}
	}
}

func TestLoadReadPolicyRejectsUnsupportedVersionAndExtraDocuments(t *testing.T) {
	tests := []string{
		"version: 2\ntables: []\n",
		"version: 1\ntables: []\n---\nversion: 1\ntables: []\n",
	}
	for _, content := range tests {
		if _, err := LoadReadPolicy(writeReadPolicyForTest(t, content)); err == nil {
			t.Fatalf("LoadReadPolicy() succeeded for invalid document %q", content)
		}
	}
}
