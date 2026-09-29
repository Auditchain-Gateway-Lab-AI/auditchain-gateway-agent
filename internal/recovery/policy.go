package recovery

import (
	"crypto/sha3"
	"fmt"
	"os"
	"sort"
	"strings"

	"auditchain-agent/internal/sqlident"
	"gopkg.in/yaml.v3"
)

type PolicyFile struct {
	Version int           `yaml:"version"`
	Tables  []TablePolicy `yaml:"tables"`
}

type TablePolicy struct {
	ResourceTable    string                  `yaml:"resource_table"`
	Schema           string                  `yaml:"schema"`
	Table            string                  `yaml:"table"`
	PrimaryKeyField  string                  `yaml:"primary_key_field"`
	PrimaryKeyColumn string                  `yaml:"primary_key_column"`
	PrimaryKeyType   string                  `yaml:"primary_key_type"`
	Operations       []Operation             `yaml:"operations"`
	Columns          map[string]ColumnPolicy `yaml:"columns"`
}

type ColumnPolicy struct {
	DatabaseColumn string `yaml:"database_column"`
	State          bool   `yaml:"state"`
	Insert         bool   `yaml:"insert"`
	Update         bool   `yaml:"update"`
	Immutable      bool   `yaml:"immutable"`
	Generated      bool   `yaml:"generated"`
	Virtual        bool   `yaml:"virtual"`
	Sensitive      bool   `yaml:"sensitive"`
}

type PolicySet struct {
	Version int
	tables  map[string]TablePolicy
}

// ReadMapping exposes only the exact projection that the verify endpoint may
// read. It intentionally contains no arbitrary predicate or caller-selected
// schema.
type ReadMapping struct {
	Schema       string
	Table        string
	PrimaryKey   string
	StateColumns []string
}

func LoadPolicy(path string) (*PolicySet, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open recovery policy: %w", err)
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var policy PolicyFile
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode recovery policy: %w", err)
	}
	if err := validatePolicy(policy); err != nil {
		return nil, err
	}

	set := &PolicySet{Version: policy.Version, tables: make(map[string]TablePolicy, len(policy.Tables))}
	for _, table := range policy.Tables {
		set.tables[strings.ToUpper(table.ResourceTable)] = table
	}
	return set, nil
}

func NewPolicySet(file PolicyFile) (*PolicySet, error) {
	if err := validatePolicy(file); err != nil {
		return nil, err
	}
	set := &PolicySet{Version: file.Version, tables: make(map[string]TablePolicy, len(file.Tables))}
	for _, table := range file.Tables {
		set.tables[strings.ToUpper(table.ResourceTable)] = table
	}
	return set, nil
}

func validatePolicy(policy PolicyFile) error {
	if policy.Version != 1 {
		return fmt.Errorf("recovery policy version must be 1")
	}
	if len(policy.Tables) == 0 {
		return fmt.Errorf("recovery policy must contain at least one table")
	}
	seenTables := make(map[string]struct{}, len(policy.Tables))
	for i := range policy.Tables {
		table := &policy.Tables[i]
		if !sqlident.IsValid(table.ResourceTable) || !sqlident.IsValid(table.Schema) || !sqlident.IsValid(table.Table) || !sqlident.IsValid(table.PrimaryKeyColumn) {
			return fmt.Errorf("table policy %q has an invalid identifier", table.ResourceTable)
		}
		if strings.TrimSpace(table.PrimaryKeyField) == "" || strings.TrimSpace(table.PrimaryKeyType) == "" {
			return fmt.Errorf("table policy %q has an incomplete primary key mapping", table.ResourceTable)
		}
		key := strings.ToUpper(table.ResourceTable)
		if _, exists := seenTables[key]; exists {
			return fmt.Errorf("duplicate recovery table policy %q", table.ResourceTable)
		}
		seenTables[key] = struct{}{}
		if len(table.Columns) == 0 {
			return fmt.Errorf("table policy %q must contain columns", table.ResourceTable)
		}

		operations := make(map[Operation]struct{}, len(table.Operations))
		for _, operation := range table.Operations {
			if !operation.Valid() {
				return fmt.Errorf("table policy %q contains invalid operation %q", table.ResourceTable, operation)
			}
			operations[operation] = struct{}{}
		}
		if len(operations) == 0 {
			return fmt.Errorf("table policy %q must allow an operation", table.ResourceTable)
		}

		canonicalFields := make(map[string]struct{}, len(table.Columns))
		databaseColumns := make(map[string]string, len(table.Columns))
		stateCount := 0
		for field, column := range table.Columns {
			normalizedField := strings.ToLower(strings.TrimSpace(field))
			if normalizedField == "" || normalizedField != field {
				return fmt.Errorf("table policy %q has a non-canonical field %q", table.ResourceTable, field)
			}
			if _, exists := canonicalFields[normalizedField]; exists {
				return fmt.Errorf("table policy %q has duplicate field %q", table.ResourceTable, field)
			}
			canonicalFields[normalizedField] = struct{}{}
			if !sqlident.IsValid(column.DatabaseColumn) {
				return fmt.Errorf("table policy %q field %q has an invalid database column", table.ResourceTable, field)
			}
			databaseKey := strings.ToUpper(column.DatabaseColumn)
			if previous, exists := databaseColumns[databaseKey]; exists {
				return fmt.Errorf("table policy %q maps %q and %q to the same database column", table.ResourceTable, previous, field)
			}
			databaseColumns[databaseKey] = field
			if isSensitiveName(field) || isSensitiveName(column.DatabaseColumn) || column.Generated || column.Virtual || column.Sensitive {
				return fmt.Errorf("table policy %q field %q is sensitive or generated and cannot be recovered", table.ResourceTable, field)
			}
			if column.State {
				stateCount++
			}
		}
		if _, exists := canonicalFields[table.PrimaryKeyField]; !exists {
			return fmt.Errorf("table policy %q primary key field %q is not mapped", table.ResourceTable, table.PrimaryKeyField)
		}
		pk := table.Columns[table.PrimaryKeyField]
		if !pk.State || !pk.Insert || !pk.Immutable || !strings.EqualFold(pk.DatabaseColumn, table.PrimaryKeyColumn) {
			return fmt.Errorf("table policy %q primary key mapping must be state, insert, immutable, and match primary_key_column", table.ResourceTable)
		}
		if stateCount == 0 {
			return fmt.Errorf("table policy %q must expose at least one state column", table.ResourceTable)
		}
		if _, ok := operations[OperationUpsert]; !ok {
			if _, ok := operations[OperationDelete]; !ok {
				return fmt.Errorf("table policy %q must allow UPSERT or DELETE", table.ResourceTable)
			}
		}
	}
	return nil
}

func isSensitiveName(name string) bool {
	value := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, "-", "_"), " ", "_"))
	for _, part := range strings.Split(value, "_") {
		switch part {
		case "password", "passwd", "secret", "token", "credential", "credentials", "apikey", "api", "private":
			return true
		}
	}
	return strings.Contains(value, "password") || strings.Contains(value, "credential")
}

func (p *PolicySet) Lookup(resourceTable string) (TablePolicy, bool) {
	if p == nil {
		return TablePolicy{}, false
	}
	table, ok := p.tables[strings.ToUpper(resourceTable)]
	return table, ok
}

func (p *PolicySet) ReadMapping(resourceTable string) (ReadMapping, bool) {
	table, ok := p.Lookup(resourceTable)
	if !ok {
		return ReadMapping{}, false
	}
	columns := make([]string, 0, len(table.Columns))
	for _, field := range sortedStateFields(table) {
		columns = append(columns, table.Columns[field].DatabaseColumn)
	}
	return ReadMapping{
		Schema:       table.Schema,
		Table:        table.Table,
		PrimaryKey:   table.PrimaryKeyColumn,
		StateColumns: columns,
	}, true
}

func sortedStateFields(table TablePolicy) []string {
	fields := make([]string, 0, len(table.Columns))
	for field, column := range table.Columns {
		if column.State {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	return fields
}

func (p *PolicySet) PolicyHash() (string, error) {
	if p == nil {
		return "", fmt.Errorf("nil recovery policy")
	}
	keys := make([]string, 0, len(p.tables))
	for key := range p.tables {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	canonical := make([]TablePolicy, 0, len(keys))
	for _, key := range keys {
		table := p.tables[key]
		table.Operations = append([]Operation(nil), table.Operations...)
		sort.Slice(table.Operations, func(i, j int) bool { return table.Operations[i] < table.Operations[j] })
		canonical = append(canonical, table)
	}
	return hashPolicy(canonical)
}

func (p *PolicySet) AllowedOperation(resourceTable string, operation Operation) bool {
	table, ok := p.Lookup(resourceTable)
	if !ok {
		return false
	}
	for _, allowed := range table.Operations {
		if allowed == operation {
			return true
		}
	}
	return false
}

func hashPolicy(value any) (string, error) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	// The policy hash is an audit identifier, not a state hash. SHA3 keeps it
	// independent from the canonical-state preimage while remaining standard.
	sum := sha3.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}
