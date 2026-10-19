package redaction

import "testing"

func TestJSONRedactsSensitiveFieldsRecursively(t *testing.T) {
	got, err := JSON([]byte(`{"id":7,"password":"clear","nested":{"api-token":"abc"},"items":[{"secret":"value"}],"count":1}`))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	want := `{"count":1,"id":7,"items":[{"secret":"***REDACTED***"}],"nested":{"api-token":"***REDACTED***"},"password":"***REDACTED***"}`
	if string(got) != want {
		t.Fatalf("redacted JSON = %s, want %s", got, want)
	}
}

func TestJSONRejectsTrailingPayload(t *testing.T) {
	if _, err := JSON([]byte(`{"id":1} {"secret":"x"}`)); err == nil {
		t.Fatal("JSON() accepted trailing payload")
	}
}
