package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// GatewayClient communicates with the auditchain-api gateway endpoints.
type GatewayClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewGatewayClient creates a new client for auditchain-api gateway.
func NewGatewayClient(baseURL, token string, timeout time.Duration) *GatewayClient {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &GatewayClient{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// GetOffchainLogs fetches audit logs within a time range from GET /api/agent/offchain-data.
func (c *GatewayClient) GetOffchainLogs(ctx context.Context, from, to time.Time) ([]OffchainLog, error) {
	endpoint := fmt.Sprintf("%s/api/agent/offchain-data?from=%s&to=%s",
		c.baseURL,
		url.QueryEscape(from.UTC().Format(time.RFC3339)),
		url.QueryEscape(to.UTC().Format(time.RFC3339)),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(body))
	}

	var logs []OffchainLog
	if err := json.Unmarshal(body, &logs); err != nil {
		return nil, fmt.Errorf("decode offchain logs: %w (body: %s)", err, string(body))
	}

	return logs, nil
}

// GetLatestOffchainLog fetches the latest audit log for a specific resource (TABLE:ID).
func (c *GatewayClient) GetLatestOffchainLog(ctx context.Context, resource string) (*OffchainLog, error) {
	escapedResource := url.PathEscape(strings.TrimPrefix(resource, "/"))
	endpoint := fmt.Sprintf("%s/api/agent/offchain-data/%s", c.baseURL, escapedResource)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(body))
	}

	var logs []OffchainLog
	if err := json.Unmarshal(body, &logs); err != nil {
		return nil, fmt.Errorf("decode offchain log: %w (body: %s)", err, string(body))
	}

	if len(logs) == 0 {
		return nil, nil
	}

	return &logs[0], nil
}

// VerifyMerkleRoot calls POST /api/agent/verify-merkle-root to verify root against blockchain.
func (c *GatewayClient) VerifyMerkleRoot(ctx context.Context, merkleRoot, blockchainTxID string) (*VerifyMerkleRootResponse, error) {
	endpoint := fmt.Sprintf("%s/api/agent/verify-merkle-root", c.baseURL)

	payload := VerifyMerkleRootRequest{
		MerkleRoot:     merkleRoot,
		BlockchainTxID: blockchainTxID,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.setHeaders(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(body))
	}

	var result VerifyMerkleRootResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode verify merkle root response: %w (body: %s)", err, string(body))
	}

	return &result, nil
}

func (c *GatewayClient) setHeaders(req *http.Request) {
	if c.token != "" {
		if strings.HasPrefix(c.token, "Bearer ") {
			req.Header.Set("Authorization", c.token)
		} else {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}
}
