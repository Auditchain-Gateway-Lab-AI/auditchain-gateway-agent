package canonicalstate

import (
	"bytes"
	"strings"
	"testing"
)

func TestCanonicalizeJSONNormalizesKeysAndNumbers(t *testing.T) {
	first, err := CanonicalizeJSON([]byte(`{" NAMA ":"ruang","ID":1.0,"ratio":0.001,"nested":{"CODE":1e0}}`))
	if err != nil {
		t.Fatalf("first canonicalization failed: %v", err)
	}
	second, err := CanonicalizeJSON([]byte(`{"nama":"ruang","id":1,"ratio":1e-3,"nested":{"code":1}}`))
	if err != nil {
		t.Fatalf("second canonicalization failed: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("canonical forms differ: %s != %s", first, second)
	}
}

func TestCanonicalizeJSONRejectsTrailingData(t *testing.T) {
	if _, err := CanonicalizeJSON([]byte(`{"id":1} trailing`)); err == nil {
		t.Fatal("trailing data must be rejected")
	}
}

func TestCanonicalizeJSONRejectsNormalizedKeyCollision(t *testing.T) {
	_, err := CanonicalizeJSON([]byte(`{"Nama":"a"," nama ":"b"}`))
	if err == nil || !strings.Contains(err.Error(), ErrDuplicateKey.Error()) {
		t.Fatalf("error = %v, want normalized duplicate-key error", err)
	}
}

func TestHashJSONIncludesTenantAndResource(t *testing.T) {
	state := []byte(`{"id":81,"nama":"ruang"}`)
	first, _, err := HashJSON("client-a", "RUANGAN:81", state)
	if err != nil {
		t.Fatalf("HashJSON() error = %v", err)
	}
	if first == "" || len(first) != 64 {
		t.Fatalf("hash = %q, want SHA3-256 hex", first)
	}
	second, _, err := HashJSON("client-b", "RUANGAN:81", state)
	if err != nil {
		t.Fatalf("HashJSON() error = %v", err)
	}
	if first == second {
		t.Fatal("different tenants must not share the same state hash")
	}
}

func TestCanonicalizeObjectRejectsNonObject(t *testing.T) {
	if _, _, err := CanonicalizeObject([]byte(`[1,2,3]`)); err != ErrExpectedObject {
		t.Fatalf("error = %v, want %v", err, ErrExpectedObject)
	}
}
