package canonicalstate

import (
	"strings"
	"testing"
)

func TestCanonicalizeJSONNormalizesKeysAndNumbers(t *testing.T) {
	got, err := CanonicalizeJSON([]byte(`{" B ":1.0,"a":{" X ":1e0},"items":[1,2.00]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"x":1},"b":1,"items":[1,2]}`
	if string(got) != want {
		t.Fatalf("canonical JSON = %s, want %s", got, want)
	}
}

func TestCanonicalizeJSONPreservesLargeInteger(t *testing.T) {
	input := `{"value":900719925474099312345678901234567890}`
	got, err := CanonicalizeJSON([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "900719925474099312345678901234567890") {
		t.Fatalf("large integer was changed: %s", got)
	}
}

func TestCanonicalizeJSONRejectsCollisionAndTrailingData(t *testing.T) {
	if _, err := CanonicalizeJSON([]byte(`{"Name":1," name ":2}`)); err == nil {
		t.Fatal("expected normalized key collision")
	}
	if _, err := CanonicalizeJSON([]byte(`{"name":1} {}`)); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestHashStateSeparatesResource(t *testing.T) {
	state := map[string]any{"id": "1", "name": "Room"}
	first, _, err := HashState("client-a", "ROOM:1", state)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := HashState("client-a", "ROOM:2", state)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 64 {
		t.Fatalf("resource hash separation failed: %q %q", first, second)
	}
}
