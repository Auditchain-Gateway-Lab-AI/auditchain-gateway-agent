package verify

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ReadPolicyFile is a read-only allowlist; it grants no recovery operations.
type ReadPolicyFile struct {
	Version int               `yaml:"version"`
	Tables  []ReadTablePolicy `yaml:"tables"`
}

type ReadTablePolicy struct {
	ResourceTable    string   `yaml:"resource_table"`
	Schema           string   `yaml:"schema"`
	Table            string   `yaml:"table"`
	PrimaryKeyColumn string   `yaml:"primary_key_column"`
	StateColumns     []string `yaml:"state_columns"`
}

type ReadPolicySet struct {
	projections map[string]ReadProjection
}

// LoadReadPolicy reads a strict, fail-closed set of Oracle read projections.
func LoadReadPolicy(path string) (*ReadPolicySet, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("verify read policy path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open verify read policy: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var document ReadPolicyFile
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode verify read policy: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("verify read policy must contain one YAML document")
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported verify read policy version %d", document.Version)
	}

	policy := &ReadPolicySet{projections: make(map[string]ReadProjection, len(document.Tables))}
	for index, table := range document.Tables {
		resource := strings.TrimSpace(table.ResourceTable)
		schema := strings.TrimSpace(table.Schema)
		actualTable := strings.TrimSpace(table.Table)
		pk := strings.TrimSpace(table.PrimaryKeyColumn)
		if !IsValidSQLIdentifier(resource) || !IsValidSQLIdentifier(schema) ||
			!IsValidSQLIdentifier(actualTable) || !IsValidSQLIdentifier(pk) {
			return nil, fmt.Errorf("verify read policy table[%d] has an invalid identifier", index)
		}
		key := strings.ToUpper(resource)
		if _, exists := policy.projections[key]; exists {
			return nil, fmt.Errorf("duplicate verify read policy resource_table %q", resource)
		}
		if len(table.StateColumns) == 0 {
			return nil, fmt.Errorf("verify read policy %q must define state_columns", resource)
		}

		columns := make([]string, 0, len(table.StateColumns))
		seen := make(map[string]struct{}, len(table.StateColumns))
		hasPK := false
		for _, configured := range table.StateColumns {
			column := strings.TrimSpace(configured)
			if !IsValidSQLIdentifier(column) || isCredentialColumn(column) {
				return nil, fmt.Errorf("verify read policy %q has an invalid or sensitive column %q", resource, configured)
			}
			columnKey := strings.ToUpper(column)
			if _, exists := seen[columnKey]; exists {
				return nil, fmt.Errorf("verify read policy %q has duplicate column %q", resource, column)
			}
			seen[columnKey] = struct{}{}
			columns = append(columns, column)
			hasPK = hasPK || columnKey == strings.ToUpper(pk)
		}
		if !hasPK {
			return nil, fmt.Errorf("verify read policy %q must include its primary key in state_columns", resource)
		}
		policy.projections[key] = ReadProjection{
			Schema: schema, Table: actualTable, PrimaryKey: pk, StateColumns: columns,
		}
	}
	return policy, nil
}

func (p *ReadPolicySet) ReadMapping(resourceTable string) (ReadProjection, bool) {
	if p == nil {
		return ReadProjection{}, false
	}
	projection, ok := p.projections[strings.ToUpper(strings.TrimSpace(resourceTable))]
	projection.StateColumns = append([]string(nil), projection.StateColumns...)
	return projection, ok
}

func (p *ReadPolicySet) TableCount() int {
	if p == nil {
		return 0
	}
	return len(p.projections)
}

func isCredentialColumn(column string) bool {
	name := strings.ToUpper(strings.NewReplacer("_", "", "-", "").Replace(column))
	for _, marker := range []string{"PASSWORD", "PASSWD", "SECRET", "TOKEN", "CREDENTIAL", "APIKEY", "PRIVATEKEY"} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}
