package agent_audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const gatewayAgentPath = "/api/agent"

// GatewayClient calls the Besu Gateway API. PostgreSQL and Fabric credentials
// remain on Besu; the Agent holds only its tenant API key.
type GatewayClient struct {
	baseURL  string
	apiKey   string
	clientID string
	http     *http.Client
}

func NewGatewayClient(baseURL, apiKey, clientID string) (*GatewayClient, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("GATEWAY_URL harus berupa URL HTTP(S) tanpa kredensial atau query")
	}
	if len(apiKey) < 72 {
		return nil, errors.New("API key Gateway tidak valid")
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("client ID wajib diisi")
	}
	httpClient := &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Never forward a tenant API key across an HTTP redirect.
			return http.ErrUseLastResponse
		},
	}
	return &GatewayClient{
		baseURL:  strings.TrimRight(parsed.String(), "/"),
		apiKey:   apiKey,
		clientID: clientID,
		http:     httpClient,
	}, nil
}

func (g *GatewayClient) LatestByResource(ctx context.Context, clientID, resource string) ([]json.RawMessage, error) {
	if err := g.checkClientID(clientID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(resource) == "" || strings.Contains(resource, "/") {
		return nil, errors.New("resource tidak valid")
	}
	target := g.baseURL + gatewayAgentPath + "/offchain-data/" + url.PathEscape(resource)
	return g.readLogs(ctx, target, maxResponseRecords)
}

func (g *GatewayClient) ByTimeRange(ctx context.Context, clientID string, from, to time.Time, limit int) ([]json.RawMessage, error) {
	if err := g.checkClientID(clientID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxResponseRecords || from.After(to) {
		return nil, errors.New("rentang atau batas log tidak valid")
	}
	target, err := url.Parse(g.baseURL + gatewayAgentPath + "/offchain-data")
	if err != nil {
		return nil, fmt.Errorf("buat URL Gateway: %w", err)
	}
	query := target.Query()
	query.Set("from", from.Format(time.RFC3339Nano))
	query.Set("to", to.Format(time.RFC3339Nano))
	target.RawQuery = query.Encode()
	return g.readLogs(ctx, target.String(), limit)
}

func (g *GatewayClient) readLogs(ctx context.Context, target string, limit int) ([]json.RawMessage, error) {
	body, err := g.request(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	var logs []json.RawMessage
	if err := json.Unmarshal(body, &logs); err != nil {
		return nil, fmt.Errorf("respons data offchain Besu tidak valid: %w", err)
	}
	if len(logs) > limit {
		return nil, errors.New("rentang hasil Besu melebihi batas yang diizinkan")
	}
	return logs, nil
}

func (g *GatewayClient) VerifyMerkleRoot(anchorID, expectedRoot string) (string, error) {
	if strings.TrimSpace(anchorID) == "" || len(anchorID) > 128 {
		return "", errors.New("blockchain_tx_id tidak valid")
	}
	if _, ok := normalizedRoot(expectedRoot); !ok {
		return "", errors.New("merkle_root tidak valid")
	}
	payload, err := json.Marshal(verifyMerkleRequest{
		MerkleRoot:     expectedRoot,
		BlockchainTxID: anchorID,
	})
	if err != nil {
		return "", fmt.Errorf("encode request verifikasi Fabric: %w", err)
	}
	body, err := g.request(context.Background(), http.MethodPost,
		g.baseURL+gatewayAgentPath+"/verify-merkle-root", payload)
	if err != nil {
		return "", err
	}
	var result struct {
		ActualRoot string `json:"actual_merkle_root"`
		AnchorID   string `json:"blockchain_tx_id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("respons verifikasi Fabric Besu tidak valid: %w", err)
	}
	if result.AnchorID != anchorID {
		return "", errors.New("Besu mengembalikan anchor ID yang berbeda")
	}
	if _, ok := normalizedRoot(result.ActualRoot); !ok {
		return "", errors.New("Besu tidak mengembalikan Merkle root yang valid")
	}
	return result.ActualRoot, nil
}

func (g *GatewayClient) checkClientID(clientID string) error {
	if g == nil || g.http == nil {
		return errors.New("client API Gateway belum diinisialisasi")
	}
	if clientID != g.clientID {
		return errors.New("client ID tidak sesuai konfigurasi Agent")
	}
	return nil
}

func (g *GatewayClient) request(ctx context.Context, method, target string, payload []byte) ([]byte, error) {
	if err := g.checkClientID(g.clientID); err != nil {
		return nil, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("buat request ke Besu Gateway: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Client-ID", g.clientID)
	request.Header.Set("api-key", g.apiKey)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := g.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("request ke Besu Gateway gagal: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("baca respons Besu Gateway: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("respons Besu Gateway melebihi batas ukuran")
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Besu Gateway mengembalikan HTTP %d", response.StatusCode)
	}
	return body, nil
}
