// Package agentverifier mengimplementasikan verifikasi Lapis 3:
// Gateway memanggil Agent yang sudah berjalan di sisi klien untuk
// mengambil data aktual dari audit_trail, lalu membandingkannya
// dengan isi AuditLog yang tersimpan di DB middleware.
package agentverifier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go-blockchain-api/internal/models"

	"gorm.io/gorm"
)

// AuditTrailRecord adalah respons dari endpoint GET /verify/<table>/<id> di Agent.
// Field-field ini mencerminkan kolom tabel audit_trail di DB klien.
type AuditTrailRecord struct {
	Found    bool                   `json:"found"`
	ID       int                    `json:"id"`
	Tabel    string                 `json:"tabel"`
	Operasi  string                 `json:"operasi"`
	DBUser   string                 `json:"db_user"`
	AppUser  *string                `json:"app_user"`
	DataLama map[string]interface{} `json:"data_lama"`
	DataBaru map[string]interface{} `json:"data_baru"`
	Waktu    time.Time              `json:"waktu"`
}

// Discrepancy mencatat satu perbedaan antara audit log di middleware vs data dari Agent
type Discrepancy struct {
	Field   string `json:"field"`
	InLog   string `json:"in_log"`
	InAgent string `json:"in_agent"`
}

// VerifyResult adalah hasil verifikasi Lapis 3
type VerifyResult struct {
	IsMatch        bool
	SourceFound    bool
	AgentUsed      bool
	Discrepancies  []Discrepancy
	AgentRecord    *AuditTrailRecord
	ClientMetadata string
	// ClientStateHash is the canonical hash of the live operational row (or
	// the canonical empty object when the row is missing). It is deliberately
	// separate from the AuditLog leaf hash so source-state incidents can be
	// correlated without pretending the client row is itself a Merkle leaf.
	ClientStateHash string
}

// Service mengelola request verifikasi ke Agent klien
type Service struct {
	db *gorm.DB
}

// ResourceRecord adalah response dari endpoint /verify/<table>/<id> di Agent.
type ResourceRecord struct {
	Found     bool                   `json:"found"`
	Table     string                 `json:"table"`
	ID        string                 `json:"id"`
	Data      map[string]interface{} `json:"data"`
	CheckedAt time.Time              `json:"checked_at"`
}

func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// VerifyAgainstAgent adalah entry point Lapis 3.
//
// Alur:
//  1. Cek apakah log punya SourceRecordID (audit_trail_id dari Agent).
//     Jika kosong → log bukan dari Agent, lapis ini dilewati.
//  2. Ambil AgentConfig klien dari DB.
//     Jika belum dikonfigurasi → lapis ini dilewati.
//  3. Panggil GET <agent_url>/verify/<table>/<source_record_id>.
//  4. Bandingkan field kunci: tabel↔resource, operasi↔action, app_user/db_user↔actor,
//     serta metadata (data_lama+data_baru) ↔ metadata di log.
func (s *Service) VerifyAgainstAgent(auditLog *models.AuditLog) (*VerifyResult, error) {
	cfg, err := s.loadAgentConfig(auditLog.ClientID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &VerifyResult{IsMatch: true, SourceFound: false, AgentUsed: false}, nil
		}
		return nil, fmt.Errorf("gagal memuat konfigurasi Agent: %w", err)
	}

	if !cfg.IsActive {
		return &VerifyResult{IsMatch: true, SourceFound: false, AgentUsed: false}, nil
	}

	if cfg.AgentURL == "" {
		return &VerifyResult{IsMatch: true, AgentUsed: false}, nil
	}

	// Jika AgentURL mengarah ke port 8083 (Debezium Native CDC), kita tahu ini bukan Custom Agent Lapis 3.
	// Debezium tidak memiliki endpoint /verify, sehingga kita skip verifikasi agent untuk klien ini.
	if strings.Contains(cfg.AgentURL, ":8083") {
		return &VerifyResult{IsMatch: true, AgentUsed: false}, nil
	}

	// Mode 1: SIMRS — verifikasi via audit_trail_id (source_record_id)
	if auditLog.SourceRecordID != "" {
		return s.verifyViaAuditTrail(cfg, auditLog)
	}

	// Mode 2: Satu Peta — verifikasi via resource (format: tabel:id)
	if auditLog.Resource != "" && strings.Contains(auditLog.Resource, ":") {
		return s.verifyViaResource(cfg, auditLog)
	}

	// Tidak ada yang bisa diverifikasi — lewati
	return &VerifyResult{IsMatch: true, SourceFound: false, AgentUsed: false}, nil
}

// verifyViaAuditTrail — mode SIMRS, query ke /verify/<table>/<source_record_id>
func (s *Service) verifyViaAuditTrail(cfg *models.AgentConfig, auditLog *models.AuditLog) (*VerifyResult, error) {
	tableName := auditLog.Resource
	recordID := auditLog.SourceRecordID
	if strings.Contains(tableName, ":") {
		parts := strings.SplitN(tableName, ":", 2)
		tableName = parts[0]
		if len(parts) == 2 && parts[1] != "" {
			// Endpoint Agent membutuhkan ID row pada tabel, bukan audit_trail_id.
			recordID = parts[1]
		}
	}
	if tableName == "" || recordID == "" {
		return nil, fmt.Errorf("resource tabel untuk verifikasi Agent kosong")
	}

	agentRec, err := s.fetchFromAgent(cfg, tableName, recordID)
	if err != nil {
		return nil, fmt.Errorf("gagal menghubungi Agent: %w", err)
	}

	if !agentRec.Found {
		return &VerifyResult{
			IsMatch:     false,
			SourceFound: false,
			AgentUsed:   true,
			Discrepancies: []Discrepancy{{
				Field:   "existence",
				InLog:   fmt.Sprintf("audit_trail.id=%s", auditLog.SourceRecordID),
				InAgent: "(baris tidak ditemukan di audit_trail klien)",
			}},
		}, nil
	}

	agentMeta := map[string]interface{}{}
	if agentRec.DataLama != nil {
		agentMeta["data_lama"] = agentRec.DataLama
	}
	if agentRec.DataBaru != nil {
		agentMeta["data_baru"] = agentRec.DataBaru
	}
	clientMetadata := marshalToJSON(agentMeta)

	discrepancies := s.compareFields(auditLog, agentRec)
	return &VerifyResult{
		IsMatch:        len(discrepancies) == 0,
		SourceFound:    true,
		AgentUsed:      true,
		Discrepancies:  discrepancies,
		AgentRecord:    agentRec,
		ClientMetadata: clientMetadata,
	}, nil
}

// verifyViaResource menangani verifikasi untuk log yang membawa Resource
// berformat "table:id" — ini mencakup SIMRS Morbis maupun Satu Peta, karena
// keduanya mengisi field Resource dengan format yang sama saat masuk lewat
// Kafka consumer (lihat kafkaconsumer/consumer.go dan consumer/consumer.go).
// Endpoint Agent yang dipanggil: GET /verify/<table>/<id>.
func (s *Service) verifyViaResource(cfg *models.AgentConfig, auditLog *models.AuditLog) (*VerifyResult, error) {
	// Parse resource: "nama_tabel:id"
	parts := strings.SplitN(auditLog.Resource, ":", 2)
	if len(parts) != 2 {
		return &VerifyResult{IsMatch: true, SourceFound: false, AgentUsed: false}, nil
	}
	tableName := parts[0]
	resourceID := parts[1]

	// Panggil Agent: GET /verify/<table>/<id>
	resourceRec, err := s.fetchResourceFromAgent(cfg, tableName, resourceID)
	if err != nil {
		return nil, fmt.Errorf("gagal menghubungi Agent untuk resource: %w", err)
	}

	// Jika action adalah DELETE, baris memang tidak boleh ada lagi
	if auditLog.Action == "DELETE" {
		if !resourceRec.Found {
			clientHash, _ := HashResourceState(auditLog.ClientID, auditLog.Resource, map[string]interface{}{})
			return &VerifyResult{
				IsMatch:         true,
				SourceFound:     false,
				AgentUsed:       true,
				ClientStateHash: clientHash,
			}, nil
		}
		clientHash, _ := HashResourceState(auditLog.ClientID, auditLog.Resource, resourceRec.Data)
		// Baris masih ada padahal sudah di-DELETE — anomali
		return &VerifyResult{
			IsMatch:         false,
			SourceFound:     true,
			AgentUsed:       true,
			ClientStateHash: clientHash,
			Discrepancies: []Discrepancy{{
				Field:   "existence",
				InLog:   "DELETE — baris seharusnya tidak ada",
				InAgent: fmt.Sprintf("baris masih ditemukan di tabel %s id=%s", tableName, resourceID),
			}},
		}, nil
	}

	// Untuk INSERT/UPDATE — baris harus ada
	if !resourceRec.Found {
		clientHash, _ := HashResourceState(auditLog.ClientID, auditLog.Resource, map[string]interface{}{})
		return &VerifyResult{
			IsMatch:         false,
			SourceFound:     false,
			AgentUsed:       true,
			ClientStateHash: clientHash,
			Discrepancies: []Discrepancy{{
				Field:   "existence",
				InLog:   fmt.Sprintf("%s — baris seharusnya ada", auditLog.Action),
				InAgent: fmt.Sprintf("baris tidak ditemukan di tabel %s id=%s", tableName, resourceID),
			}},
		}, nil
	}

	// Bandingkan metadata log dengan data aktual dari Agent
	discrepancies := s.CompareResourceData(auditLog, resourceRec)
	clientMetadata := marshalToJSON(resourceRec.Data)
	clientHash, _ := HashResourceState(auditLog.ClientID, auditLog.Resource, resourceRec.Data)
	return &VerifyResult{
		IsMatch:         len(discrepancies) == 0,
		SourceFound:     true,
		AgentUsed:       true,
		Discrepancies:   discrepancies,
		ClientMetadata:  clientMetadata,
		ClientStateHash: clientHash,
	}, nil
}

// fetchResourceFromAgent memanggil GET <agent_url>/verify/<table>/<id>
func (s *Service) fetchResourceFromAgent(cfg *models.AgentConfig, tableName, resourceID string) (*ResourceRecord, error) {
	return s.fetchResourceFromAgentContext(context.Background(), cfg, tableName, resourceID)
}

func (s *Service) fetchResourceFromAgentContext(ctx context.Context, cfg *models.AgentConfig, tableName, resourceID string) (*ResourceRecord, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	url := agentVerifyURL(cfg.AgentURL, tableName, resourceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if cfg.VerifyToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.VerifyToken)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request ke Agent gagal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("token verifikasi Agent tidak valid (401)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Agent mengembalikan status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca response Agent: %w", err)
	}

	var rec ResourceRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("gagal parse response Agent: %w", err)
	}

	return &rec, nil
}

// CompareResourceData membandingkan metadata di log dengan data aktual dari Agent
func (s *Service) CompareResourceData(auditLog *models.AuditLog, rec *ResourceRecord) []Discrepancy {
	var diffs []Discrepancy

	if auditLog.Metadata == "" || auditLog.Metadata == "{}" {
		return diffs
	}

	// Parse metadata log — ambil bagian data_baru saja
	var logMeta map[string]interface{}
	if err := json.Unmarshal([]byte(auditLog.Metadata), &logMeta); err != nil {
		return diffs
	}

	// Bandingkan field per field
	skipFields := map[string]bool{
		"ogc_fid": true, "id": true, "_id": true,
		"fid": true, "gid": true, "objectid": true,
	}

	for key, logVal := range logMeta {
		if skipFields[strings.ToLower(key)] {
			continue
		}
		agentVal, exists := rec.Data[key]
		if !exists {
			continue
		}

		strLogVal := formatValueToString(logVal)
		strAgentVal := formatValueToString(agentVal)

		strLogVal, strAgentVal = tryNormalizeTimeMatch(strLogVal, strAgentVal)

		if strLogVal != strAgentVal {
			diffs = append(diffs, Discrepancy{
				Field:   key,
				InLog:   strLogVal,
				InAgent: strAgentVal,
			})
		}
	}

	return diffs
}

// loadAgentConfig mengambil konfigurasi Agent dari DB middleware
func (s *Service) loadAgentConfig(clientID string) (*models.AgentConfig, error) {
	var cfg models.AgentConfig
	err := s.db.
		Where("client_id = ? AND is_active = true AND deleted_at IS NULL", clientID).
		First(&cfg).Error
	return &cfg, err
}

// fetchFromAgent memanggil GET <agent_url>/verify/<table>/<source_record_id>
func (s *Service) fetchFromAgent(cfg *models.AgentConfig, tableName, sourceRecordID string) (*AuditTrailRecord, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	url := agentVerifyURL(cfg.AgentURL, tableName, sourceRecordID)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if cfg.VerifyToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.VerifyToken)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request ke Agent gagal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("token verifikasi Agent tidak valid (401) — periksa AGENT_VERIFY_TOKEN")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Agent mengembalikan status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca response Agent: %w", err)
	}

	var rec AuditTrailRecord
	if err := json.Unmarshal(body, &rec); err != nil {
		return nil, fmt.Errorf("gagal parse response Agent: %w", err)
	}

	return &rec, nil
}

func agentVerifyURL(agentURL, tableName, resourceID string) string {
	baseURL := strings.TrimRight(agentURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return fmt.Sprintf("%s/verify/%s/%s", baseURL, tableName, resourceID)
}

// compareFields membandingkan field-field penting antara AuditLog di middleware
// dengan data aktual dari Agent. Mapping dilakukan sesuai ClientFieldMapping:
//   - audit_trail.tabel    ↔ AuditLog.Resource
//   - audit_trail.operasi  ↔ AuditLog.Action
//   - audit_trail.app_user (fallback db_user) ↔ AuditLog.Actor
//   - gabungan data_lama+data_baru ↔ AuditLog.Metadata
func (s *Service) compareFields(auditLog *models.AuditLog, agent *AuditTrailRecord) []Discrepancy {
	var diffs []Discrepancy

	// Resource ↔ tabel
	if auditLog.Resource != agent.Tabel {
		diffs = append(diffs, Discrepancy{
			Field:   "resource/tabel",
			InLog:   auditLog.Resource,
			InAgent: agent.Tabel,
		})
	}

	// Action ↔ operasi
	if auditLog.Action != agent.Operasi {
		diffs = append(diffs, Discrepancy{
			Field:   "action/operasi",
			InLog:   auditLog.Action,
			InAgent: agent.Operasi,
		})
	}

	// Actor ↔ app_user (fallback ke db_user) — sesuai ClientFieldMapping klien
	sourceActor := agent.DBUser
	if agent.AppUser != nil && *agent.AppUser != "" {
		sourceActor = *agent.AppUser
	}
	if auditLog.Actor != sourceActor {
		diffs = append(diffs, Discrepancy{
			Field:   "actor/app_user",
			InLog:   auditLog.Actor,
			InAgent: sourceActor,
		})
	}

	// Metadata ↔ gabungan data_lama + data_baru
	// Agent selalu mengirim metadata sebagai {"data_lama": {...}, "data_baru": {...}}
	if auditLog.Metadata != "" {
		agentMeta := map[string]interface{}{}
		if agent.DataLama != nil {
			agentMeta["data_lama"] = agent.DataLama
		}
		if agent.DataBaru != nil {
			agentMeta["data_baru"] = agent.DataBaru
		}

		logMetaNorm := normalizeJSON(auditLog.Metadata)
		agentMetaNorm := marshalToJSON(agentMeta)

		if logMetaNorm != agentMetaNorm {
			diffs = append(diffs, Discrepancy{
				Field:   "metadata",
				InLog:   logMetaNorm,
				InAgent: agentMetaNorm,
			})
		}
	}

	return diffs
}

func formatValueToString(val interface{}) string {
	if val == nil {
		return "null"
	}

	switch v := val.(type) {
	case float64:
		if v == math.Trunc(v) {
			return fmt.Sprintf("%.0f", v)
		}
		return fmt.Sprintf("%g", v)
	case json.Number:
		return v.String()
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

func tryNormalizeTimeMatch(strLog, strAgent string) (string, string) {
	if strLog == strAgent {
		return strLog, strAgent
	}

	var timeLog, timeAgent time.Time
	var errLog, errAgent error

	// Coba parse Agent time sebagai ISO-8601 (RFC3339)
	timeAgent, errAgent = time.Parse(time.RFC3339, strAgent)
	if errAgent != nil {
		return strLog, strAgent
	}

	// Parse Log time
	timeLog, errLog = time.Parse(time.RFC3339, strLog)
	if errLog != nil {
		// Coba parse sebagai angka epoch (dari Debezium)
		epochFloat, err := strconv.ParseFloat(strLog, 64)
		if err == nil {
			if epochFloat > 1e14 {
				// Microseconds
				timeLog = time.Unix(0, int64(epochFloat*1000)).UTC()
			} else if epochFloat > 1e11 {
				// Milliseconds
				timeLog = time.UnixMilli(int64(epochFloat)).UTC()
			} else {
				// Seconds
				timeLog = time.Unix(int64(epochFloat), 0).UTC()
			}
		} else {
			return strLog, strAgent
		}
	}

	// Bandingkan selisih waktu
	diff := timeLog.Sub(timeAgent)
	if diff < 0 {
		diff = -diff
	}

	// Jika selisih max 1 detik (toleransi presisi/pembulatan), anggap cocok
	if diff <= time.Second {
		return timeAgent.Format(time.RFC3339), strAgent
	}

	return strLog, strAgent
}

func normalizeJSON(raw string) string {
	var m interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return raw
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func marshalToJSON(m map[string]interface{}) string {
	b, _ := json.Marshal(m)
	return string(b)
}

// FetchTableData memuat konfigurasi lalu memanggil FetchTableFromAgent
func (s *Service) FetchTableData(clientID, tableName string) ([]ResourceRecord, error) {
	cfg, err := s.loadAgentConfig(clientID)
	if err != nil {
		return nil, fmt.Errorf("gagal memuat konfigurasi Agent: %w", err)
	}
	if !cfg.IsActive {
		return nil, fmt.Errorf("agent tidak aktif")
	}
	return s.FetchTableFromAgent(cfg, tableName)
}

// FetchTableFromAgent memanggil GET <agent_url>/table/<table>
func (s *Service) FetchTableFromAgent(cfg *models.AgentConfig, tableName string) ([]ResourceRecord, error) {
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}

	url := agentFetchTableURL(cfg.AgentURL, tableName)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	if cfg.VerifyToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.VerifyToken)
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request ke Agent gagal: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("token verifikasi Agent tidak valid (401)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Agent mengembalikan status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20))
	if err != nil {
		return nil, fmt.Errorf("gagal membaca response Agent: %w", err)
	}

	var records []ResourceRecord
	if err := json.Unmarshal(body, &records); err != nil {
		return nil, fmt.Errorf("gagal parse response Agent: %w", err)
	}

	return records, nil
}

func agentFetchTableURL(agentURL, tableName string) string {
	baseURL := strings.TrimRight(agentURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return fmt.Sprintf("%s/table/%s", baseURL, tableName)
}
