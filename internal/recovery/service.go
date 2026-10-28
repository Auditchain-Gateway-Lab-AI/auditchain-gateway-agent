package recovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"auditchain-agent/internal/sqlident"
	"auditchain-agent/internal/verify"
)

// Service provides single-row data recovery for client Oracle database.
type Service struct {
	db             *sql.DB
	readProjection func(string) (verify.ReadProjection, bool)
}

// NewService creates a new recovery Service instance.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SetReadProjection configures policy projection lookup from verify-policy.yml.
func (s *Service) SetReadProjection(lookup func(string) (verify.ReadProjection, bool)) {
	s.readProjection = lookup
}

// RecoverRow restores a single row of a table to the provided clean state.
func (s *Service) RecoverRow(ctx context.Context, table, recordID string, data map[string]interface{}) (*RecoverResult, error) {
	if s.db == nil {
		return nil, errors.New("database connection unavailable")
	}

	table = strings.TrimSpace(table)
	recordID = strings.TrimSpace(recordID)
	if table == "" || recordID == "" {
		return nil, errors.New("table and recordID are required")
	}

	if !sqlident.IsValid(table) {
		return nil, fmt.Errorf("invalid table name %q", table)
	}

	if len(data) == 0 {
		return nil, errors.New("data payload cannot be empty")
	}

	// 1. Resolve table, schema, and primary key column
	owner, actualTable, pkCol, err := s.resolveTableInfo(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("resolve table metadata: %w", err)
	}

	qualifiedTable := actualTable
	if owner != "" {
		qualifiedTable = fmt.Sprintf("%s.%s", sqlident.Quote(owner), sqlident.Quote(actualTable))
	} else {
		qualifiedTable = sqlident.Quote(actualTable)
	}

	// 2. Check if row currently exists
	checkQuery := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = :1", qualifiedTable, sqlident.Quote(pkCol))
	var dummy int
	checkErr := s.db.QueryRowContext(ctx, checkQuery, recordID).Scan(&dummy)
	rowExists := checkErr == nil

	// 3. Prepare columns to recover
	var cols []string
	for k := range data {
		colName := strings.ToUpper(strings.TrimSpace(k))
		if strings.EqualFold(colName, pkCol) {
			continue // Skip PK in updates
		}
		// On UPDATE, skip creation metadata columns if row already exists
		if rowExists && (strings.EqualFold(colName, "CREATED_AT") || strings.EqualFold(colName, "USER_INPUT")) {
			continue
		}
		if sqlident.IsValid(colName) {
			cols = append(cols, colName)
		}
	}
	sort.Strings(cols)

	if len(cols) == 0 && rowExists {
		return &RecoverResult{
			Status:           "SUCCESS",
			Table:            table,
			ID:               recordID,
			Operation:        "NOOP",
			RecoveredColumns: nil,
			Timestamp:        time.Now(),
			Message:          "no updateable columns provided",
		}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	operation := "UPDATE"
	if rowExists {
		// UPDATE row
		setClauses := make([]string, 0, len(cols))
		args := make([]interface{}, 0, len(cols)+1)

		for i, col := range cols {
			setClauses = append(setClauses, fmt.Sprintf("%s = :%d", sqlident.Quote(col), i+1))
			args = append(args, oracleValue(col, findValue(data, col)))
		}
		args = append(args, recordID)

		updateQuery := fmt.Sprintf("UPDATE %s SET %s WHERE %s = :%d",
			qualifiedTable,
			strings.Join(setClauses, ", "),
			sqlident.Quote(pkCol),
			len(args),
		)

		log.Printf("[Recovery] Executing: %s with %d args", updateQuery, len(args))
		res, execErr := tx.ExecContext(ctx, updateQuery, args...)
		if execErr != nil {
			return nil, fmt.Errorf("update row: %w", execErr)
		}
		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			return nil, errors.New("update affected 0 rows")
		}
	} else {
		// INSERT missing row
		operation = "INSERT"
		insertCols := make([]string, 0, len(cols)+1)
		placeholders := make([]string, 0, len(cols)+1)
		args := make([]interface{}, 0, len(cols)+1)

		// PK first
		insertCols = append(insertCols, sqlident.Quote(pkCol))
		placeholders = append(placeholders, fmt.Sprintf(":%d", len(args)+1))
		args = append(args, recordID)

		for _, col := range cols {
			insertCols = append(insertCols, sqlident.Quote(col))
			placeholders = append(placeholders, fmt.Sprintf(":%d", len(args)+1))
			args = append(args, oracleValue(col, findValue(data, col)))
		}

		insertQuery := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
			qualifiedTable,
			strings.Join(insertCols, ", "),
			strings.Join(placeholders, ", "),
		)

		log.Printf("[Recovery] Executing: %s with %d args", insertQuery, len(args))
		_, execErr := tx.ExecContext(ctx, insertQuery, args...)
		if execErr != nil {
			return nil, fmt.Errorf("insert row: %w", execErr)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit recovery: %w", err)
	}

	return &RecoverResult{
		Status:           "SUCCESS",
		Table:            table,
		ID:               recordID,
		Operation:        operation,
		RecoveredColumns: cols,
		Timestamp:        time.Now(),
		Message:          fmt.Sprintf("successfully %sd row in %s", strings.ToLower(operation), table),
	}, nil
}

func (s *Service) resolveTableInfo(ctx context.Context, table string) (owner, actualTable, pkCol string, err error) {
	if s.readProjection != nil {
		if proj, ok := s.readProjection(table); ok {
			return proj.Schema, proj.Table, proj.PrimaryKey, nil
		}
	}

	// Dynamic lookup in Oracle dictionary
	err = s.db.QueryRowContext(ctx, `
		SELECT owner, table_name
		FROM (
			SELECT owner, table_name
			FROM all_tables
			WHERE UPPER(table_name) = UPPER(:1)
			ORDER BY CASE WHEN owner = USER THEN 0 ELSE 1 END, owner
		)
		WHERE rownum = 1
	`, table).Scan(&owner, &actualTable)
	if err != nil {
		actualTable = table
		pkCol = "ID"
		return "", actualTable, pkCol, nil
	}

	// Query PK from constraints
	err = s.db.QueryRowContext(ctx, `
		SELECT cols.column_name
		FROM all_constraints cons
		JOIN all_cons_columns cols
		  ON cons.constraint_name = cols.constraint_name
		 AND cons.owner = cols.owner
		WHERE cons.constraint_type = 'P'
		  AND cons.owner = :1
		  AND UPPER(cols.table_name) = UPPER(:2)
		  AND rownum = 1
	`, owner, actualTable).Scan(&pkCol)
	if err != nil || pkCol == "" {
		pkCol = "ID"
	}

	return owner, actualTable, pkCol, nil
}

func findValue(data map[string]interface{}, colName string) interface{} {
	for k, v := range data {
		if strings.EqualFold(k, colName) {
			return v
		}
	}
	return nil
}

func oracleValue(colName string, value interface{}) interface{} {
	if value == nil {
		return nil
	}

	// Unwrap json.Number if present
	if num, ok := value.(json.Number); ok {
		if i, err := num.Int64(); err == nil {
			value = i
		} else if f, err := num.Float64(); err == nil {
			value = f
		} else {
			value = num.String()
		}
	}

	upperCol := strings.ToUpper(colName)
	isTimestampCol := strings.HasSuffix(upperCol, "_AT") ||
		strings.HasSuffix(upperCol, "_DATE") ||
		strings.HasSuffix(upperCol, "_TIME") ||
		strings.Contains(upperCol, "TANGGAL") ||
		upperCol == "CREATED_AT" || upperCol == "UPDATED_AT"

	if isTimestampCol {
		switch v := value.(type) {
		case int64:
			if v > 100000000000000 { // Microseconds (> 1e14)
				return time.UnixMicro(v).UTC()
			} else if v > 100000000000 { // Milliseconds (> 1e11)
				return time.UnixMilli(v).UTC()
			} else if v > 100000000 { // Seconds (> 1e8)
				return time.Unix(v, 0).UTC()
			}
		case int:
			if int64(v) > 100000000 {
				return time.Unix(int64(v), 0).UTC()
			}
		case float64:
			if v > 100000000000000 {
				return time.UnixMicro(int64(v)).UTC()
			} else if v > 100000000000 {
				return time.UnixMilli(int64(v)).UTC()
			} else if v > 100000000 {
				return time.Unix(int64(v), 0).UTC()
			}
		case string:
			str := strings.TrimSpace(v)
			if str == "" {
				return nil
			}
			layouts := []string{
				time.RFC3339Nano,
				time.RFC3339,
				"2006-01-02 15:04:05.999999999",
				"2006-01-02 15:04:05",
				"2006-01-02",
			}
			for _, layout := range layouts {
				if t, err := time.Parse(layout, str); err == nil {
					return t.UTC()
				}
			}
		case time.Time:
			return v.UTC()
		}
	}

	switch value.(type) {
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return value
	default:
		return fmt.Sprintf("%v", value)
	}
}
