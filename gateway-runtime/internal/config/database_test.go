package config

import (
	"strings"
	"testing"
)

func TestOpenDBRejectsBlankDSNWithoutConnecting(t *testing.T) {
	db, err := OpenDB(" \t\n")
	if err == nil {
		t.Fatal("OpenDB() expected an error for a blank DSN")
	}
	if db != nil {
		t.Fatal("OpenDB() returned a database handle for a blank DSN")
	}
}

func TestMigrateDBRequiresConnection(t *testing.T) {
	if err := MigrateDB(nil); err == nil || !strings.Contains(err.Error(), "connection is required") {
		t.Fatalf("MigrateDB(nil) error = %v, want connection-required error", err)
	}
}

func TestValidateDBSchemaRequiresConnection(t *testing.T) {
	if err := ValidateDBSchema(nil); err == nil || !strings.Contains(err.Error(), "connection is required") {
		t.Fatalf("ValidateDBSchema(nil) error = %v, want connection-required error", err)
	}
}
