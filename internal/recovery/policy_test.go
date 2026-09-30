package recovery

import "testing"

func testPolicyFile() PolicyFile {
	return PolicyFile{
		Version: 1,
		Tables: []TablePolicy{{
			ResourceTable:    "ROOM",
			Schema:           "CLIENT",
			Table:            "ROOM",
			PrimaryKeyField:  "id",
			PrimaryKeyColumn: "ID",
			PrimaryKeyType:   "string",
			Operations:       []Operation{OperationUpsert, OperationDelete},
			Columns: map[string]ColumnPolicy{
				"id":   {DatabaseColumn: "ID", State: true, Insert: true, Immutable: true},
				"name": {DatabaseColumn: "NAME", State: true, Insert: true, Update: true},
			},
		}},
	}
}

func TestPolicyRejectsSensitiveAndDuplicateColumns(t *testing.T) {
	policy := testPolicyFile()
	policy.Tables[0].Columns["password"] = ColumnPolicy{DatabaseColumn: "PASSWORD", State: true, Insert: true}
	if _, err := NewPolicySet(policy); err == nil {
		t.Fatal("expected sensitive column rejection")
	}

	policy = testPolicyFile()
	policy.Tables[0].Columns["alias"] = ColumnPolicy{DatabaseColumn: "NAME", State: true, Insert: true}
	if _, err := NewPolicySet(policy); err == nil {
		t.Fatal("expected duplicate database column rejection")
	}
}

func TestPolicyLookupIsCaseInsensitiveAndReadMappingIsExact(t *testing.T) {
	set, err := NewPolicySet(testPolicyFile())
	if err != nil {
		t.Fatal(err)
	}
	mapping, ok := set.ReadMapping("room")
	if !ok || mapping.Schema != "CLIENT" || mapping.PrimaryKey != "ID" {
		t.Fatalf("unexpected mapping: %#v %v", mapping, ok)
	}
	if len(mapping.StateColumns) != 2 || mapping.StateColumns[0] != "ID" || mapping.StateColumns[1] != "NAME" {
		t.Fatalf("unexpected state columns: %#v", mapping.StateColumns)
	}
}
