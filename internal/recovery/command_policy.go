package recovery

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"auditchain-agent/internal/verify"
	"gopkg.in/yaml.v3"
)

var canonicalFieldPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type CommandPolicyFile struct {
	Version int                  `yaml:"version"`
	Tables  []CommandTablePolicy `yaml:"tables"`
}

type CommandTablePolicy struct {
	ResourceTable    string                         `yaml:"resource_table"`
	Schema           string                         `yaml:"schema"`
	Table            string                         `yaml:"table"`
	PrimaryKeyField  string                         `yaml:"primary_key_field"`
	PrimaryKeyColumn string                         `yaml:"primary_key_column"`
	Operations       []string                       `yaml:"operations"`
	Columns          map[string]CommandColumnPolicy `yaml:"columns"`
}

type CommandColumnPolicy struct {
	DatabaseColumn string `yaml:"database_column"`
	State          bool   `yaml:"state"`
	Insert         bool   `yaml:"insert"`
	Update         bool   `yaml:"update"`
	Immutable      bool   `yaml:"immutable"`
}

type CommandPolicySet struct {
	tables map[string]CommandTablePolicy
}

func LoadCommandPolicy(path string, readMapping func(string) (verify.ReadProjection, bool)) (*CommandPolicySet, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recovery policy: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var document CommandPolicyFile
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode recovery policy: %w", err)
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("recovery policy must contain one YAML document")
	}
	if document.Version != 1 {
		return nil, fmt.Errorf("unsupported recovery policy version %d", document.Version)
	}
	if len(document.Tables) == 0 {
		return nil, fmt.Errorf("recovery policy has no enabled table mappings")
	}

	policy := &CommandPolicySet{tables: make(map[string]CommandTablePolicy, len(document.Tables))}
	for index, table := range document.Tables {
		resource := strings.ToUpper(strings.TrimSpace(table.ResourceTable))
		schema := strings.TrimSpace(table.Schema)
		actualTable := strings.TrimSpace(table.Table)
		pkField := strings.TrimSpace(table.PrimaryKeyField)
		pkColumn := strings.TrimSpace(table.PrimaryKeyColumn)
		if !verify.IsValidSQLIdentifier(resource) || !verify.IsValidSQLIdentifier(schema) ||
			!verify.IsValidSQLIdentifier(actualTable) || !verify.IsValidSQLIdentifier(pkColumn) ||
			!canonicalFieldPattern.MatchString(pkField) {
			return nil, fmt.Errorf("recovery policy table[%d] has an invalid identifier or primary key field", index)
		}
		key := strings.ToUpper(resource)
		if _, exists := policy.tables[key]; exists {
			return nil, fmt.Errorf("duplicate recovery resource table %q", resource)
		}
		projection, ok := readMapping(resource)
		if !ok {
			return nil, fmt.Errorf("recovery resource %q is not present in the read policy", resource)
		}
		if !strings.EqualFold(projection.Schema, schema) || !strings.EqualFold(projection.Table, actualTable) || !strings.EqualFold(projection.PrimaryKey, pkColumn) {
			return nil, fmt.Errorf("recovery table %q must match its verify read mapping", resource)
		}
		if len(table.Columns) == 0 {
			return nil, fmt.Errorf("recovery table %q has no column allowlist", resource)
		}

		seenColumns := make(map[string]string, len(table.Columns))
		stateColumns := make(map[string]struct{}, len(projection.StateColumns))
		for _, column := range projection.StateColumns {
			stateColumns[strings.ToUpper(strings.TrimSpace(column))] = struct{}{}
		}
		pkPolicy, pkFound := table.Columns[pkField]
		if !pkFound || !pkPolicy.State || !pkPolicy.Insert || pkPolicy.Update || !pkPolicy.Immutable || !strings.EqualFold(pkPolicy.DatabaseColumn, pkColumn) {
			return nil, fmt.Errorf("recovery table %q primary key policy must be state=true, insert=true, update=false, immutable=true", resource)
		}
		for field, column := range table.Columns {
			if !canonicalFieldPattern.MatchString(field) || !verify.IsValidSQLIdentifier(strings.TrimSpace(column.DatabaseColumn)) {
				return nil, fmt.Errorf("recovery table %q has invalid canonical field or database column", resource)
			}
			columnKey := strings.ToUpper(strings.TrimSpace(column.DatabaseColumn))
			if strings.Contains(columnKey, "PASSWORD") || strings.Contains(columnKey, "TOKEN") || strings.Contains(columnKey, "SECRET") || strings.Contains(columnKey, "CREDENTIAL") {
				return nil, fmt.Errorf("recovery table %q includes a sensitive column", resource)
			}
			if previous, exists := seenColumns[columnKey]; exists {
				return nil, fmt.Errorf("recovery table %q maps %q and %q to the same database column", resource, previous, field)
			}
			seenColumns[columnKey] = field
			if column.State {
				if _, exists := stateColumns[columnKey]; !exists {
					return nil, fmt.Errorf("recovery state column %q is not in the verify projection", field)
				}
				delete(stateColumns, columnKey)
			}
			if column.Update && column.Immutable {
				return nil, fmt.Errorf("recovery immutable column %q cannot be updateable", field)
			}
			if (column.Insert || column.Update) && !column.State {
				return nil, fmt.Errorf("recovery writable column %q must be a state column", field)
			}
		}
		if len(stateColumns) != 0 {
			return nil, fmt.Errorf("recovery table %q must map every verify projection column", resource)
		}
		operations := make([]string, 0, len(table.Operations))
		seenOperations := make(map[string]struct{})
		for _, operation := range table.Operations {
			operation = strings.ToUpper(strings.TrimSpace(operation))
			if operation != "UPSERT" && operation != "DELETE" {
				return nil, fmt.Errorf("recovery table %q contains unsupported operation %q", resource, operation)
			}
			if _, exists := seenOperations[operation]; exists {
				return nil, fmt.Errorf("recovery table %q contains duplicate operation %q", resource, operation)
			}
			seenOperations[operation] = struct{}{}
			operations = append(operations, operation)
		}
		if len(operations) == 0 {
			return nil, fmt.Errorf("recovery table %q has no allowed operations", resource)
		}
		table.ResourceTable = resource
		table.Schema = schema
		table.Table = actualTable
		table.PrimaryKeyField = pkField
		table.PrimaryKeyColumn = pkColumn
		table.Operations = operations
		policy.tables[key] = table
	}
	return policy, nil
}

func (p *CommandPolicySet) table(resourceTable string) (CommandTablePolicy, bool) {
	if p == nil {
		return CommandTablePolicy{}, false
	}
	table, ok := p.tables[strings.ToUpper(strings.TrimSpace(resourceTable))]
	return table, ok
}

func (t CommandTablePolicy) allows(operation string) bool {
	for _, allowed := range t.Operations {
		if strings.EqualFold(allowed, operation) {
			return true
		}
	}
	return false
}
