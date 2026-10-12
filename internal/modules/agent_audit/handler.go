// Package agent_audit exposes a small, read-only view of Gateway audit data
// and Fabric anchors from the client-side Agent.
package agent_audit

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	offchainPath       = "/api/agent/offchain-data"
	verifyMerklePath   = "/api/agent/verify-merkle-root"
	maxResponseRecords = 1000
	maxResponseBytes   = 4 << 20
	maxRequestBody     = 64 * 1024
	requestTimeout     = 15 * time.Second
)

type OffchainStore interface {
	LatestByResource(ctx context.Context, clientID, resource string) ([]json.RawMessage, error)
	ByTimeRange(ctx context.Context, clientID string, from, to time.Time, limit int) ([]json.RawMessage, error)
}

type FabricAnchorReader interface {
	QueryMerkleRoot(anchorID string) (string, error)
}

type Handler struct {
	clientID string
	token    string
	store    OffchainStore
	fabric   FabricAnchorReader
}

func NewHandler(clientID, token string, store OffchainStore, fabric FabricAnchorReader) (*Handler, error) {
	if strings.TrimSpace(clientID) == "" {
		return nil, errConfig("client ID is required")
	}
	if strings.TrimSpace(token) == "" {
		return nil, errConfig("Agent verify token is required")
	}
	if store == nil {
		return nil, errConfig("offchain store is required")
	}
	if fabric == nil {
		return nil, errConfig("Fabric anchor reader is required")
	}
	return &Handler{clientID: clientID, token: token, store: store, fabric: fabric}, nil
}

type configError string

func (e configError) Error() string { return string(e) }

func errConfig(message string) error { return configError(message) }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == offchainPath || strings.HasPrefix(r.URL.Path, offchainPath+"/"):
		h.handleOffchainData(w, r)
	case r.URL.Path == verifyMerklePath:
		h.handleVerifyMerkleRoot(w, r)
	default:
		writeError(w, http.StatusNotFound, "route tidak ditemukan")
	}
}

func (h *Handler) handleOffchainData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method tidak diizinkan")
		return
	}
	if !h.authorized(w, r, true) {
		return
	}

	fromText, toText := r.URL.Query().Get("from"), r.URL.Query().Get("to")
	var (
		logs []json.RawMessage
		err  error
	)
	queryCtx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	if fromText != "" || toText != "" {
		if fromText == "" || toText == "" {
			writeError(w, http.StatusBadRequest, "parameter from dan to harus dikirim bersama")
			return
		}
		from, fromErr := time.Parse(time.RFC3339Nano, fromText)
		to, toErr := time.Parse(time.RFC3339Nano, toText)
		if fromErr != nil || toErr != nil || from.After(to) {
			writeError(w, http.StatusBadRequest, "rentang waktu tidak valid; gunakan RFC3339 dan from <= to")
			return
		}
		logs, err = h.store.ByTimeRange(queryCtx, h.clientID, from, to, maxResponseRecords+1)
	} else {
		resource := strings.TrimPrefix(r.URL.Path, offchainPath)
		resource = strings.TrimPrefix(resource, "/")
		if resource == "" || strings.Contains(resource, "/") {
			writeError(w, http.StatusBadRequest, "gunakan resource pada path atau parameter from dan to")
			return
		}
		logs, err = h.store.LatestByResource(queryCtx, h.clientID, resource)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "gagal membaca data offchain")
		return
	}
	if len(logs) > maxResponseRecords {
		writeError(w, http.StatusUnprocessableEntity, "rentang berisi terlalu banyak log; persempit rentang waktu")
		return
	}
	payload, err := json.Marshal(logs)
	if err != nil {
		writeError(w, http.StatusBadGateway, "data offchain tidak dapat diserialisasi")
		return
	}
	if len(payload) > maxResponseBytes {
		writeError(w, http.StatusUnprocessableEntity, "hasil terlalu besar; persempit rentang waktu")
		return
	}
	writeJSONBytes(w, http.StatusOK, payload)
}

type verifyMerkleRequest struct {
	MerkleRoot     string `json:"merkle_root"`
	BlockchainTxID string `json:"blockchain_tx_id"`
}

func (h *Handler) handleVerifyMerkleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method tidak diizinkan")
		return
	}
	if !h.authorized(w, r, false) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request verifyMerkleRequest
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "format request tidak valid")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "request harus berisi satu objek JSON")
		return
	}
	request.BlockchainTxID = strings.TrimSpace(request.BlockchainTxID)
	if request.BlockchainTxID == "" || len(request.BlockchainTxID) > 128 {
		writeError(w, http.StatusBadRequest, "blockchain_tx_id tidak valid")
		return
	}
	expectedRoot, ok := normalizedRoot(request.MerkleRoot)
	if !ok {
		writeError(w, http.StatusBadRequest, "merkle_root harus berupa nilai 32-byte dalam format hexadecimal")
		return
	}

	actualText, err := h.fabric.QueryMerkleRoot(request.BlockchainTxID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "gagal membaca anchor dari Fabric")
		return
	}
	actualRoot, ok := normalizedRoot(actualText)
	if !ok {
		writeError(w, http.StatusBadGateway, "Fabric mengembalikan Merkle root yang tidak valid")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"is_match":             bytes.Equal(expectedRoot, actualRoot),
		"expected_merkle_root": strings.TrimSpace(request.MerkleRoot),
		"actual_merkle_root":   actualText,
		"blockchain_tx_id":     request.BlockchainTxID,
	})
}

func (h *Handler) authorized(w http.ResponseWriter, r *http.Request, requireClientID bool) bool {
	const prefix = "Bearer "
	provided := r.Header.Get("Authorization")
	if !strings.HasPrefix(provided, prefix) || !secureEqual(strings.TrimPrefix(provided, prefix), h.token) {
		writeError(w, http.StatusUnauthorized, "token Agent tidak valid")
		return false
	}
	clientID := r.Header.Get("X-Client-ID")
	if requireClientID && clientID == "" {
		writeError(w, http.StatusUnauthorized, "header X-Client-ID wajib diisi")
		return false
	}
	if clientID != "" && clientID != h.clientID {
		writeError(w, http.StatusForbidden, "client ID tidak diizinkan")
		return false
	}
	return true
}

func secureEqual(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func normalizedRoot(root string) ([]byte, bool) {
	root = strings.TrimSpace(root)
	if len(root) != 64 {
		return nil, false
	}
	decoded, err := hex.DecodeString(root)
	return decoded, err == nil && len(decoded) == 32
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSONBytes(w, status, payload)
}

func writeJSONBytes(w http.ResponseWriter, status int, payload []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}
