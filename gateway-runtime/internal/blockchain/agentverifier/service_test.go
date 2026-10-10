package agentverifier

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go-blockchain-api/internal/models"
)

func TestFetchResourceFromAgentPreservesHTTPStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	service := &Service{}
	_, err := service.fetchResourceFromAgentContext(context.Background(), &models.AgentConfig{
		AgentURL: server.URL, TimeoutSeconds: 2,
	}, "AGAMA", "723")
	if err == nil {
		t.Fatal("expected Agent HTTP status error")
	}

	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T (%v), want *HTTPStatusError", err, err)
	}
	if statusErr.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP status = %d, want %d", statusErr.StatusCode, http.StatusForbidden)
	}
}
