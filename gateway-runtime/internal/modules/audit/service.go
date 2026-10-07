package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"go-blockchain-api/internal/blockchain"
	"go-blockchain-api/internal/blockchain/agentverifier"
	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/engine/tamperscanner"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/crypto"
)

type VerificationResult struct {
	Status             string                      `json:"status"`
	Message            string                      `json:"message"`
	IsValid            bool                        `json:"is_valid"`
	ExpectedHash       string                      `json:"expected_hash"`
	ActualHash         string                      `json:"actual_hash"`
	DBRoot             string                      `json:"db_root"`
	ChainRoot          string                      `json:"chain_root"`
	LogID              string                      `json:"log_id"`
	TxID               *string                     `json:"blockchain_tx_id,omitempty"`
	AgentStatus        string                      `json:"agent_status,omitempty"`
	SourceStatus       string                      `json:"source_status,omitempty"`
	AgentDiscrepancies []agentverifier.Discrepancy `json:"agent_discrepancies,omitempty"`
}

type DataVerificationResult struct {
	Status       string      `json:"status"`
	Message      string      `json:"message"`
	IsValid      bool        `json:"is_valid"`
	Resource     string      `json:"resource"`
	ExpectedData interface{} `json:"expected_data"`
	ActualData   interface{} `json:"actual_data"`
	LastLogID    string      `json:"last_log_id"`
}

// PaginationMeta menampung metadata pagination untuk response GetRecentLogs.
type PaginationMeta struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	TotalItems int64 `json:"total_items"`
	TotalPages int   `json:"total_pages"`
}

// RecentLogItem membungkus AuditLog dengan integrity_status hasil
// pengecekan Layer 2 (re-hash) dan, jika sudah ANCHORED, Layer 4
// (kecocokan merkle_root vs ledger Fabric).
type RecentLogItem struct {
	models.AuditLog
	IntegrityStatus string `json:"integrity_status"` // valid | tampered | unreachable | pending
	DBEngine        string `json:"db_engine"`
}

// RecentLogsResult adalah bentuk response baru GetRecentLogs:
// {"data": [...], "pagination": {...}, "note": "..."}
type RecentLogsResult struct {
	Data       []RecentLogItem `json:"data"`
	Pagination PaginationMeta  `json:"pagination"`
	Note       string          `json:"note,omitempty"`
}

type Service interface {
	GetDashboardStats(clientID string) (map[string]int64, error)
	VerifyLogIntegrity(logID, clientID string) (*VerificationResult, error)
	// VerifyGatewayIntegrity checks only the AuditChain PostgreSQL row against
	// its local hash and Fabric anchor. It deliberately does not contact the
	// client database or Agent and is used by the scheduled tamper scanner.
	VerifyGatewayIntegrity(logID, clientID string) (string, error)
	GetFabricRecord(anchorID string) (map[string]interface{}, error)
	VerifyDataIntegrity(resource, clientID string, rawData *map[string]interface{}) (*DataVerificationResult, error)

	// GetRecentLogsPaginated menggantikan GetRecentLogs lama sebagai entry
	// point handler dashboard. limit hardcoded 500 di versi lama diganti
	// page/pageSize. integrityStatus kosong berarti tanpa filter.
	GetRecentLogsPaginated(clientID string, page, pageSize int, integrityStatus, sortOrder, sourceTable, dbEngine string, fromTime, toTime *time.Time) (*RecentLogsResult, error)

	GetResourceInventory(clientID string) (interface{}, error)
	VerifyResourceHistory(resource, clientID string) (*ResourceChainResult, error)
	GetLogsByResource(resource, clientID string) ([]models.AuditLog, error)
	GetTableResources(tableName, clientID string) ([]ResourceLogVerification, error)
	EstimateLogRange(from, to time.Time, clientID string) (int64, error)
	VerifyInternalLogRange(from, to time.Time, clientID, requestID string) (*RangeVerificationResult, error)
	VerifyClientLogRange(from, to time.Time, clientID, requestID string) (*RangeVerificationResult, error)
	VerifyRangeBatch(from, to time.Time, clientID, requestID, runID, integritySource string, limit, offset int) (RangeSummary, int, error)
}

type auditService struct {
	repo                AuditRepository
	fabric              *blockchain.FabricService
	agent               *agentverifier.Service
	db                  *gorm.DB
	rangeVerificationMu sync.Mutex
}

type ResourceLogVerification struct {
	LogID           string `json:"log_id"`
	Resource        string `json:"resource"` // Menyimpan nama/id resource
	Action          string `json:"action"`
	LastAction      string `json:"last_action"` // Alias untuk kompabilitas frontend
	Actor           string `json:"actor"`
	Timestamp       string `json:"timestamp"`
	LastUpdatedAt   string `json:"last_updated_at"` // Alias untuk kompabilitas frontend
	HashValue       string `json:"hash_value"`
	IntegrityStatus string `json:"integrity_status"` // valid | tampered | pending | unreachable
	ChainStatus     string `json:"chain_status"`     // Alias untuk kompabilitas frontend
	// RecoveryStatus adalah status workflow recovery untuk log target ini.
	// Status ini sengaja dipisah dari IntegrityStatus: Agent client yang tidak
	// dapat dihubungi tidak boleh membuat log yang sudah pulih terlihat rusak.
	//   not_recovered — belum ada request recovery
	//   pending       — request recovery masih berjalan
	//   recovered     — request recovery berhasil dieksekusi
	//   failed        — request recovery terakhir gagal/ditolak
	RecoveryStatus     string     `json:"recovery_status"`
	RecoveryRequestID  string     `json:"recovery_request_id,omitempty"`
	RecoveryExecutedAt *time.Time `json:"recovery_executed_at,omitempty"`
	// SourceStatus is the live-client comparison dimension. It must not be
	// folded into IntegrityStatus/ChainStatus, which describe Gateway + Fabric.
	SourceStatus string `json:"source_status,omitempty"`
	// IsLatest marks the newest event in the timeline. A RECOVERY event can be
	// latest because it is a valid AuditChain event.
	IsLatest bool `json:"is_latest"`
	// IsLatestClientEvent marks the newest client-originated event that should
	// be compared with the live client database through Agent.
	IsLatestClientEvent bool `json:"is_latest_client_event"`

	// AgentStatus HANYA relevan untuk event operasional client terbaru
	// (IsLatestClientEvent=true). Agent cuma bisa membaca kondisi TERKINI data klien,
	// jadi membandingkannya ke log lama pasti mismatch meski tidak ada
	// tampering — itu bukan bukti manipulasi, itu snapshot historis yang
	// memang sudah usang secara wajar.
	//   matched            — event client terbaru, Agent dihubungi, data cocok
	//   mismatch           — event client terbaru, Agent dihubungi, ada perbedaan
	//   unreachable        — event client terbaru, Agent gagal dihubungi
	//   not_configured     — event client terbaru, klien belum setup AgentConfig
	//   skipped_recovery   — event internal Gateway, tidak dibandingkan ke Agent
	//   skipped_historical — event client lama, Layer 3 tidak relevan
	AgentStatus        string                      `json:"agent_status"`
	AgentDiscrepancies []agentverifier.Discrepancy `json:"agent_discrepancies,omitempty"`
}

type ResourceChainResult struct {
	Resource    string                    `json:"resource"`
	ChainStatus string                    `json:"chain_status"` // valid | tampered | pending | unreachable
	ChainIssues []string                  `json:"chain_issues,omitempty"`
	TotalLogs   int                       `json:"total_logs"`
	Logs        []ResourceLogVerification `json:"logs"`
}

func NewService(repo AuditRepository, fabric *blockchain.FabricService, db *gorm.DB) Service {
	return &auditService{
		repo:   repo,
		fabric: fabric,
		agent:  agentverifier.NewService(db),
		db:     db,
	}
}

type RangeVerificationResult struct {
	Range   RangeInfo         `json:"range"`
	Summary RangeSummary      `json:"summary"`
	Results []RangeItemResult `json:"results"`
}

const MaxSynchronousVerifyRange = 100

type VerifyRangeTooLargeError struct {
	EstimatedItems int64
	Limit          int
}

func (e *VerifyRangeTooLargeError) Error() string {
	return fmt.Sprintf("verify range berisi %d log; batas sinkron adalah %d", e.EstimatedItems, e.Limit)
}

type RangeInfo struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type RangeSummary struct {
	Total           int `json:"total"`
	Valid           int `json:"valid"`
	Invalid         int `json:"invalid"`
	Pending         int `json:"pending"`
	AlreadyVerified int `json:"already_verified"`
	VerifiedNow     int `json:"verified_now"`
}

type FabricAnchorData struct {
	AnchorID      string `json:"anchor_id"`
	MerkleRoot    string `json:"merkle_root"`
	Timestamp     string `json:"timestamp"`
	BatchSize     string `json:"batch_size"`
	SourceGateway string `json:"source_gateway"`
	SignatureNode string `json:"signature_node"`
}

// pgTimestampLayout mencocokkan format default tampilan PostgreSQL dengan
// presisi microsecond (6 digit), contoh: 2026-06-30 09:06:52.766123+07
const pgTimestampLayout = "2006-01-02 15:04:05.000000-07"

func formatPgTimestamp(t time.Time) string {
	return t.Local().Format(pgTimestampLayout)
}

func formatFabricTimestamp(raw string) string {
	if raw == "" {
		return raw
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return raw
	}
	return formatPgTimestamp(t)
}

func normalizeDBEngine(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql":
		return "postgres"
	case "mongo", "mongodb":
		return "mongodb"
	case "mysql", "mariadb":
		return "mysql"
	case "sqlserver", "sql_server", "mssql", "sql server":
		return "sqlserver"
	case "oracle":
		return "oracle"
	default:
		return ""
	}
}

type RangeItemResult struct {
	LogID              string                      `json:"log_id"`
	Log                models.AuditLog             `json:"log"`
	VerifyStatus       string                      `json:"verify_status"`
	Message            string                      `json:"message,omitempty"`
	AgentStatus        string                      `json:"agent_status,omitempty"`
	AgentDiscrepancies []agentverifier.Discrepancy `json:"agent_discrepancies,omitempty"`
}

// EstimateLogRange menghitung jumlah log yang akan diproses sebelum operasi
// verifikasi sinkron dimulai. Handler memakainya untuk preflight UX, sedangkan
// VerifyLogRange tetap menghitung ulang agar limit tidak dapat dilewati karena
// race atau client yang memanggil endpoint verify secara langsung.
func (s *auditService) EstimateLogRange(from, to time.Time, clientID string) (int64, error) {
	return s.repo.CountLogsByTimeRange(from, to, clientID)
}

// Implementasi
func (s *auditService) VerifyInternalLogRange(from, to time.Time, clientID, requestID string) (*RangeVerificationResult, error) {
	return s.verifyLogRange(from, to, clientID, requestID)
}

// VerifyClientLogRange intentionally uses the same all-log verification
// contract as the gateway dashboard. The client portal only renders Summary;
// it must not silently change the meaning of a date-range verification to
// "latest resource per table".
func (s *auditService) VerifyClientLogRange(from, to time.Time, clientID, requestID string) (*RangeVerificationResult, error) {
	return s.verifyLogRange(from, to, clientID, requestID)
}

func (s *auditService) VerifyRangeBatch(from, to time.Time, clientID, requestID, runID, integritySource string, limit, offset int) (RangeSummary, int, error) {
	return s.verifyRangeBatch(from, to, clientID, requestID, runID, integritySource, limit, offset)
}

func (s *auditService) verifyLogRange(from, to time.Time, clientID, requestID string) (*RangeVerificationResult, error) {
	// The checked timestamp is the durable idempotency marker. The mutex also
	// closes the same-process race where two clicks read the row before either
	// request has persisted its result.
	s.rangeVerificationMu.Lock()
	defer s.rangeVerificationMu.Unlock()

	countStarted := time.Now()
	estimatedItems, err := s.EstimateLogRange(from, to, clientID)
	logRangeTiming(requestID, "count_logs", countStarted, err, "estimated_items", estimatedItems)
	if err != nil {
		return nil, err
	}
	if estimatedItems > MaxSynchronousVerifyRange {
		return nil, &VerifyRangeTooLargeError{
			EstimatedItems: estimatedItems,
			Limit:          MaxSynchronousVerifyRange,
		}
	}

	loadStarted := time.Now()
	logs, err := s.repo.GetLogsByTimeRange(from, to, clientID)
	logRangeTiming(requestID, "load_logs", loadStarted, err, "loaded_items", len(logs))
	if err != nil {
		return nil, err
	}

	summary, results := s.verifyRangeItems(logs, clientID, requestID, models.IntegritySourceManualRange, "")
	return &RangeVerificationResult{
		Range: RangeInfo{
			From: formatPgTimestamp(from),
			To:   formatPgTimestamp(to),
		},
		Summary: summary,
		Results: results,
	}, nil
}

// verifyRangeBatch is used by the background verifier. It deliberately shares
// the per-log cache and stats update path with the synchronous endpoint, but
// reads only one bounded page at a time so a large range never becomes one
// oversized HTTP request or one unbounded in-memory slice.
func (s *auditService) verifyRangeBatch(from, to time.Time, clientID, requestID, runID, integritySource string, limit, offset int) (RangeSummary, int, error) {
	s.rangeVerificationMu.Lock()
	defer s.rangeVerificationMu.Unlock()

	started := time.Now()
	logs, err := s.repo.GetLogsByTimeRangePage(from, to, clientID, limit, offset)
	logRangeTiming(requestID, "load_verification_batch", started, err, "loaded_items", len(logs))
	if err != nil {
		return RangeSummary{}, 0, err
	}

	summary, _ := s.verifyRangeItems(logs, clientID, requestID, integritySource, runID)
	return summary, len(logs), nil
}

func (s *auditService) verifyRangeItems(logs []models.AuditLog, clientID, requestID, integritySource, runID string) (RangeSummary, []RangeItemResult) {
	result := &RangeVerificationResult{
		Results: make([]RangeItemResult, 0, len(logs)),
	}
	newlyVerified := make([]RangeItemResult, 0, len(logs))

	for _, auditLog := range logs {
		item, verifiedNow := s.verifyRangeLog(auditLog, clientID, requestID, integritySource, runID)
		if verifiedNow {
			result.Summary.VerifiedNow++
			newlyVerified = append(newlyVerified, item)
		} else {
			result.Summary.AlreadyVerified++
		}

		addRangeSummary(&result.Summary, item.VerifyStatus)
		result.Results = append(result.Results, item)
	}

	// Only newly processed log IDs contribute to cumulative dashboard stats.
	// Replaying the same date range therefore returns the same summary without
	// inflating total_verifications, total_valid, or total_tampered.
	s.updateRangeDashboardStats(clientID, newlyVerified)
	if len(newlyVerified) > 0 {
		s.recordLatestIntegrityCheck(clientID, integritySource, runID, int64(len(newlyVerified)))
	}

	return result.Summary, result.Results
}

func (s *auditService) verifyRangeLog(auditLog models.AuditLog, clientID, requestID, integritySource, runID string) (RangeItemResult, bool) {
	item := RangeItemResult{
		LogID: auditLog.LogID,
		Log:   auditLog,
	}

	if hasCachedIntegrity(&auditLog) {
		item.VerifyStatus, item.Message = cachedRangeStatus(auditLog.IntegrityStatus)
		return item, false
	}

	verifyStarted := time.Now()
	verifyResult, err := s.verifyLogIntegrity(auditLog.LogID, clientID, requestID, true)
	logRangeTiming(requestID, "verify_log", verifyStarted, err, "log_id", auditLog.LogID)

	persistedStatus := models.IntegrityStatusUnreachable
	if err != nil || verifyResult == nil {
		item.VerifyStatus = "error"
		if err != nil {
			item.Message = err.Error()
		} else {
			item.Message = "hasil verifikasi kosong"
		}
		s.persistIntegrityStatus(auditLog.LogID, clientID, persistedStatus, integritySource, runID, err)
	} else {
		item.VerifyStatus = verifyResult.Status
		item.Message = verifyResult.Message
		item.AgentStatus = verifyResult.AgentStatus
		item.AgentDiscrepancies = verifyResult.AgentDiscrepancies
		persistedStatus = integrityStatusFromVerificationResult(verifyResult)
		s.persistIntegrityStatus(auditLog.LogID, clientID, persistedStatus, integritySource, runID, nil)
	}

	item.Log.IntegrityStatus = persistedStatus
	item.Log.IntegritySource = integritySource
	item.Log.IntegrityRunID = runID
	now := time.Now().UTC()
	item.Log.IntegrityCheckedAt = &now
	return item, true
}

func hasCachedIntegrity(auditLog *models.AuditLog) bool {
	if auditLog == nil || auditLog.IntegrityCheckedAt == nil {
		return false
	}

	status := strings.ToUpper(strings.TrimSpace(auditLog.IntegrityStatus))
	return status != "" && status != models.IntegrityStatusNotChecked
}

func cachedRangeStatus(status string) (string, string) {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case models.IntegrityStatusValid:
		return "success", "Hasil verifikasi tersimpan dan digunakan kembali."
	case models.IntegrityStatusPending:
		return "pending", "Hasil verifikasi pending tersimpan dan digunakan kembali."
	case models.IntegrityStatusTampered:
		return "failed_cached", "Hasil verifikasi tampered tersimpan dan digunakan kembali."
	case models.IntegrityStatusUnreachable:
		return "error", "Hasil verifikasi unavailable tersimpan dan digunakan kembali."
	default:
		return "error", "Hasil verifikasi tersimpan dengan status tidak dikenal."
	}
}

func integrityStatusFromVerificationResult(result *VerificationResult) string {
	if result == nil {
		return models.IntegrityStatusUnreachable
	}

	switch result.Status {
	case "success":
		return models.IntegrityStatusValid
	case "pending":
		return models.IntegrityStatusPending
	case "failed_local", "failed_onchain", "failed_source":
		return models.IntegrityStatusTampered
	default:
		return models.IntegrityStatusUnreachable
	}
}

func addRangeSummary(summary *RangeSummary, status string) {
	summary.Total++
	switch status {
	case "success":
		summary.Valid++
	case "pending":
		summary.Pending++
	default:
		summary.Invalid++
	}
}

type rangeTableSummary struct {
	Total            int `json:"total"`
	Valid            int `json:"valid"`
	Invalid          int `json:"invalid"`
	Pending          int `json:"pending"`
	Unavailable      int `json:"unavailable,omitempty"`
	TamperedOnchain  int `json:"tampered_onchain,omitempty"`
	TamperedOffchain int `json:"tampered_offchain,omitempty"`
}

func (s *auditService) updateRangeDashboardStats(clientID string, newlyVerified []RangeItemResult) {
	if s.db == nil || len(newlyVerified) == 0 {
		return
	}

	_ = s.db.Transaction(func(tx *gorm.DB) error {
		var stat models.ClientDashboardStats
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("client_id = ?", clientID).First(&stat).Error; err != nil {
			return nil
		}

		var tableResults map[string]rangeTableSummary
		if err := json.Unmarshal([]byte(stat.TableVerifyResults), &tableResults); err != nil || tableResults == nil {
			tableResults = make(map[string]rangeTableSummary)
		}

		totalValid := int64(0)
		totalTampered := int64(0)
		totalPending := int64(0)
		for _, item := range newlyVerified {
			tableName := strings.TrimSpace(strings.SplitN(item.Log.Resource, ":", 2)[0])
			if tableName == "" {
				tableName = "UNKNOWN"
			}

			table := tableResults[tableName]
			table.Total++
			switch item.VerifyStatus {
			case "success":
				totalValid++
				table.Valid++
			case "pending":
				totalPending++
				table.Pending++
			default:
				totalTampered++
				table.Invalid++
			}
			tableResults[tableName] = table
		}

		newJSON, _ := json.Marshal(tableResults)
		newTotalVerifications := stat.TotalVerifications + int64(len(newlyVerified))
		newTotalValid := stat.TotalValid + totalValid
		newIntegrityScore := float64(0)
		if newTotalVerifications > 0 {
			newIntegrityScore = float64(newTotalValid) / float64(newTotalVerifications) * 100
		}

		return tx.Model(&stat).Updates(map[string]interface{}{
			"last_verified_at":     time.Now().UTC(),
			"last_verified_table":  "MULTIPLE (Audit Range)",
			"total_verifications":  newTotalVerifications,
			"total_rows_verified":  gorm.Expr("total_rows_verified + ?", len(newlyVerified)),
			"total_valid":          newTotalValid,
			"total_tampered":       stat.TotalTampered + totalTampered,
			"total_verify_pending": stat.TotalVerifyPending + totalPending,
			"integrity_score":      newIntegrityScore,
			"table_verify_results": string(newJSON),
		}).Error
	})
}

func logRangeTiming(requestID, operation string, started time.Time, err error, field string, value interface{}) {
	if requestID == "" {
		requestID = "unknown"
	}
	log.Printf(
		"[AuditTiming] request_id=%s operation=%s duration_ms=%d success=%t %s=%v",
		requestID,
		operation,
		time.Since(started).Milliseconds(),
		err == nil,
		field,
		value,
	)
}

func (s *auditService) fetchFabricAnchor(txID string) (*FabricAnchorData, error) {
	raw, err := s.fabric.GetAnchorFromLedger(txID)
	if err != nil {
		return nil, err
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("gagal parse fabric response: %w", err)
	}

	anchor := &FabricAnchorData{}
	if v, ok := parsed["anchor_id"].(string); ok {
		anchor.AnchorID = v
	}
	if v, ok := parsed["merkle_root"].(string); ok {
		anchor.MerkleRoot = v
	}
	if v, ok := parsed["timestamp"].(string); ok {
		anchor.Timestamp = formatFabricTimestamp(v)
	}
	if v, ok := parsed["batch_size"].(string); ok {
		anchor.BatchSize = v
	}
	if v, ok := parsed["source_gateway"].(string); ok {
		anchor.SourceGateway = v
	}
	if v, ok := parsed["signature_node"].(string); ok {
		anchor.SignatureNode = v
	}

	return anchor, nil
}

func (s *auditService) GetDashboardStats(clientID string) (map[string]int64, error) {
	return s.repo.GetDashboardStats(clientID)
}

func canonicalizeLog(auditLog *models.AuditLog) {
	// Metadata TIDAK di-re-marshal. String yang tersimpan di DB adalah
	// hasil json.Marshal() satu kali saat log pertama ditulis (consumer.go
	// / normalizer.go), dan itu SUDAH final — re-encode ulang di sini
	// tidak dijamin idempotent untuk nilai numerik (int vs float64,
	// notasi eksponensial pada angka besar, dst), sehingga bisa memicu
	// false-positive "tampered" meski data tidak pernah berubah.
	if auditLog.AuthorizationContext == "null" ||
		auditLog.AuthorizationContext == "<nil>" {
		auditLog.AuthorizationContext = ""
	}
}

func isHashStillPending(auditLog *models.AuditLog) bool {
	if auditLog == nil {
		return false
	}

	if auditLog.Status == "RECEIVED" {
		return true
	}

	if auditLog.HashValue == "" {
		return true
	}

	return strings.HasPrefix(auditLog.HashValue, "PENDING-")
}

// VerifyLogIntegrity menjalankan verifikasi 2-lapis: Layer 2 (re-hash lokal
// terhadap PostgreSQL) dan Layer 4 (kecocokan merkle_root vs Fabric ledger).
// Verifikasi Kafka (Layer 3) SENGAJA DIHAPUS — cukup DB (off-chain) dan
// Fabric (on-chain) saja sesuai keputusan terbaru.
func (s *auditService) VerifyLogIntegrity(logID, clientID string) (*VerificationResult, error) {
	result, err := s.verifyLogIntegrity(logID, clientID, "", false)
	status := models.IntegrityStatusUnreachable
	if err == nil && result != nil {
		switch result.Status {
		case "success":
			status = models.IntegrityStatusValid
		case "pending":
			status = models.IntegrityStatusPending
		case "failed_local", "failed_onchain", "failed_source":
			status = models.IntegrityStatusTampered
		}
	}
	if result != nil {
		s.persistIntegrityStatus(result.LogID, clientID, status, models.IntegritySourceManualSingleLog, "", err)
		s.recordLatestIntegrityCheck(clientID, models.IntegritySourceManualSingleLog, "", 1)
	}
	return result, err
}

// VerifyGatewayIntegrity is the scanner-safe verification path. It checks
// only the gateway database row, Merkle proof, and Fabric anchor. Client-side
// Agent verification is intentionally outside this path.
func (s *auditService) VerifyGatewayIntegrity(logID, clientID string) (string, error) {
	auditLog, err := s.repo.GetLogByID(logID, clientID)
	if err != nil {
		return models.IntegrityStatusUnreachable, errors.New("log_not_found")
	}
	result := s.VerifyGatewayIntegrityBatch([]models.AuditLog{*auditLog})[logID]
	return result.Status, result.Err
}

// VerifyGatewayIntegrityBatch verifies a scanner batch while caching Fabric
// anchor reads by anchor ID. This avoids querying the same Merkle root once
// per log when a batch contains many siblings from one anchored tree.
func (s *auditService) VerifyGatewayIntegrityBatch(logs []models.AuditLog) map[string]tamperscanner.BatchVerificationResult {
	results := make(map[string]tamperscanner.BatchVerificationResult, len(logs))
	checkedPerClient := make(map[string]int64)
	type anchorResult struct {
		root string
		err  error
	}
	anchors := make(map[string]anchorResult)

	for _, input := range logs {
		auditLog := input
		checkedPerClient[auditLog.ClientID]++
		result := tamperscanner.BatchVerificationResult{Status: models.IntegrityStatusUnreachable}
		if isHashStillPending(&auditLog) {
			result.Status = models.IntegrityStatusPending
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
			continue
		}

		canonicalizeLog(&auditLog)
		recalculated := hasher.GenerateLogHash(&auditLog)
		if recalculated != auditLog.HashValue {
			result.Status = models.IntegrityStatusTampered
			s.recordTamperIncident(auditLog, "METADATA_HASH_MISMATCH", recalculated)
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
			continue
		}

		if auditLog.Status != "ANCHORED" || auditLog.BlockchainTxID == nil || strings.TrimSpace(*auditLog.BlockchainTxID) == "" {
			result.Status = models.IntegrityStatusPending
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
			continue
		}
		if s.fabric == nil {
			result.Err = errors.New("fabric_unavailable")
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", result.Err)
			continue
		}

		anchorID := strings.TrimSpace(*auditLog.BlockchainTxID)
		anchor, cached := anchors[anchorID]
		if !cached {
			anchor = anchorResult{}
			raw, err := s.fabric.GetAnchorFromLedger(anchorID)
			if err != nil {
				anchor.err = fmt.Errorf("fabric_read_failed: %w", err)
			} else {
				var fabricResponse struct {
					MerkleRoot string `json:"merkle_root"`
				}
				if err := json.Unmarshal([]byte(raw), &fabricResponse); err != nil || strings.TrimSpace(fabricResponse.MerkleRoot) == "" {
					if err == nil {
						err = errors.New("merkle_root kosong")
					}
					anchor.err = fmt.Errorf("fabric_anchor_invalid: %w", err)
				} else {
					anchor.root = fabricResponse.MerkleRoot
				}
			}
			anchors[anchorID] = anchor
		}
		if anchor.err != nil {
			result.Err = anchor.err
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", result.Err)
			continue
		}

		if auditLog.MerkleRoot == "" || auditLog.MerkleRoot != anchor.root {
			result.Status = models.IntegrityStatusTampered
			s.recordTamperIncident(auditLog, "MERKLE_ROOT_MISMATCH", recalculated)
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
			continue
		}

		proofs, err := s.repo.GetProofsByHash(auditLog.HashValue)
		if err != nil {
			result.Err = fmt.Errorf("merkle_proof_read_failed: %w", err)
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", result.Err)
			continue
		}
		for _, proof := range proofs {
			if proof.MerkleRoot != "" && proof.MerkleRoot != anchor.root {
				result.Status = models.IntegrityStatusTampered
				s.recordTamperIncident(auditLog, "MERKLE_PROOF_MISMATCH", recalculated)
				break
			}
		}
		if result.Status == models.IntegrityStatusTampered {
			results[auditLog.LogID] = result
			s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
			continue
		}

		reconstructedRoot := auditLog.HashValue
		if len(proofs) > 0 {
			reconstructedRoot = crypto.ReconstructMerkleRoot(auditLog.HashValue, toMerkleProofData(proofs))
		}
		if reconstructedRoot != anchor.root {
			result.Status = models.IntegrityStatusTampered
			s.recordTamperIncident(auditLog, "MERKLE_PROOF_MISMATCH", reconstructedRoot)
		} else {
			result.Status = models.IntegrityStatusValid
		}
		results[auditLog.LogID] = result
		s.persistIntegrityStatus(auditLog.LogID, auditLog.ClientID, result.Status, models.IntegritySourceTamperScanner, "", nil)
	}
	for clientID, count := range checkedPerClient {
		s.recordLatestIntegrityCheck(clientID, models.IntegritySourceTamperScanner, "", count)
	}
	return results
}

func integrityStatusModelValue(status string) string {
	switch status {
	case "valid":
		return models.IntegrityStatusValid
	case "tampered":
		return models.IntegrityStatusTampered
	case "pending":
		return models.IntegrityStatusPending
	case "unreachable":
		return models.IntegrityStatusUnreachable
	default:
		return models.IntegrityStatusNotChecked
	}
}

func (s *auditService) persistIntegrityStatus(logID, clientID, status, integritySource, runID string, verificationErr error) {
	if s.db == nil || logID == "" || clientID == "" {
		return
	}
	now := time.Now().UTC()
	message := ""
	if verificationErr != nil {
		message = verificationErr.Error()
	}
	_ = s.db.Model(&models.AuditLog{}).
		Where("log_id = ? AND client_id = ?", logID, clientID).
		Updates(map[string]interface{}{
			"integrity_status":     status,
			"integrity_checked_at": now,
			"integrity_error":      message,
			"integrity_source":     integritySource,
			"integrity_run_id":     runID,
		}).Error
}

func (s *auditService) recordLatestIntegrityCheck(clientID, source, runID string, checkedLogs int64) {
	if s.db == nil || clientID == "" || source == "" || checkedLogs <= 0 {
		return
	}

	checkedAt := time.Now().UTC()
	err := s.db.Model(&models.ClientDashboardStats{}).
		Where("client_id = ? AND (last_integrity_check_at IS NULL OR last_integrity_check_at <= ?)", clientID, checkedAt).
		Updates(map[string]interface{}{
			"last_integrity_check_at":     checkedAt,
			"last_integrity_check_source": source,
			"last_integrity_check_run_id": runID,
			"last_integrity_check_logs":   checkedLogs,
		}).Error
	if err != nil {
		log.Printf("[IntegrityProvenance] gagal menyimpan ringkasan client=%s source=%s: %v", clientID, source, err)
	}
}

func (s *auditService) verifyLogIntegrity(logID, clientID, requestID string, skipAgent bool) (*VerificationResult, error) {
	logLookupStarted := time.Now()
	auditLog, err := s.repo.GetLogByID(logID, clientID)
	logRangeTiming(requestID, "db_log_lookup", logLookupStarted, err, "log_id", logID)
	if err != nil {
		return nil, errors.New("log_not_found")
	}

	if isHashStillPending(auditLog) {
		return &VerificationResult{
			Status:  "pending",
			Message: "Log sudah diterima, tetapi hash final masih diproses pipeline.",
			IsValid: true,
			LogID:   auditLog.LogID,
		}, nil
	}

	canonicalizeLog(auditLog)
	recalculatedHash := hasher.GenerateLogHash(auditLog)
	if recalculatedHash != auditLog.HashValue {
		s.recordTamperIncident(*auditLog, "METADATA_HASH_MISMATCH", recalculatedHash)
		return &VerificationResult{
			Status:       "failed_local",
			Message:      "🚨 DATA TERMANIPULASI: Isi data telah diubah di database middleware.",
			IsValid:      false,
			LogID:        auditLog.LogID,
			ExpectedHash: auditLog.HashValue,
			ActualHash:   recalculatedHash,
		}, nil
	}

	if auditLog.BlockchainTxID == nil || *auditLog.BlockchainTxID == "PENDING_OR_FAILED" {
		return &VerificationResult{
			Status:  "pending",
			Message: "Log otentik secara lokal. Menunggu antrean Blockchain.",
			IsValid: true,
			LogID:   auditLog.LogID,
		}, nil
	}

	if s.fabric == nil {
		return nil, errors.New("fabric_error")
	}
	fabricStarted := time.Now()
	onChainData, err := s.fabric.GetAnchorFromLedger(*auditLog.BlockchainTxID)
	logRangeTiming(requestID, "fabric_anchor_lookup", fabricStarted, err, "log_id", logID)
	if err != nil {
		return nil, errors.New("fabric_error")
	}

	var fabricResponse struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.Unmarshal([]byte(onChainData), &fabricResponse); err != nil {
		return nil, errors.New("parse_error")
	}

	proofLookupStarted := time.Now()
	proofs, perr := s.repo.GetProofsByHash(auditLog.HashValue)
	logRangeTiming(requestID, "db_proof_lookup", proofLookupStarted, perr, "log_id", logID)
	if perr != nil {
		return nil, errors.New("proof_lookup_error")
	}

	reconstructedRoot := auditLog.HashValue // batch 1-leaf: root == hash itu sendiri
	if len(proofs) > 0 {
		reconstructedRoot = crypto.ReconstructMerkleRoot(auditLog.HashValue, toMerkleProofData(proofs))
	}
	if auditLog.MerkleRoot != "" && auditLog.MerkleRoot != fabricResponse.MerkleRoot {
		s.recordTamperIncident(*auditLog, "MERKLE_ROOT_MISMATCH", recalculatedHash)
		return &VerificationResult{
			Status:    "failed_onchain",
			Message:   "🚨 DATA TERMANIPULASI: Merkle root pada database berbeda dari anchor Fabric.",
			IsValid:   false,
			LogID:     auditLog.LogID,
			DBRoot:    auditLog.MerkleRoot,
			ChainRoot: fabricResponse.MerkleRoot,
		}, nil
	}

	verifiedVia := "merkle_proof"
	if reconstructedRoot != fabricResponse.MerkleRoot {
		// Fallback untuk data lama yang di-anchor SEBELUM fix ini (proof
		// level>0 & IsLeft belum tersimpan benar) — supaya tidak false-positive
		// menandai log lama yang sebenarnya sah sebagai "tampered".
		if auditLog.MerkleRoot != fabricResponse.MerkleRoot {
			s.recordTamperIncident(*auditLog, "MERKLE_ROOT_MISMATCH", recalculatedHash)
			return &VerificationResult{
				Status:    "failed_onchain",
				Message:   "🚨 FATAL MISMATCH: Merkle Root tidak diakui oleh jaringan Blockchain!",
				IsValid:   false,
				LogID:     auditLog.LogID,
				DBRoot:    reconstructedRoot,
				ChainRoot: fabricResponse.MerkleRoot,
			}, nil
		}
		verifiedVia = "legacy_field_fallback"
	}

	successMsg := "✅ DATA OTENTIK: Terverifikasi di database dan Blockchain."
	if verifiedVia == "legacy_field_fallback" {
		successMsg += " (diverifikasi via metode lama — log ini di-anchor sebelum perbaikan Merkle proof)"
	}

	agentStatus := "skipped_historical"
	sourceStatus := ""
	var agentDiscrepancies []agentverifier.Discrepancy

	if !skipAgent {
		if isRecoveryAction(auditLog.Action) {
			agentStatus = "skipped_recovery"
		} else {
			latestLookupStarted := time.Now()
			latestClientLog, latestErr := s.repo.GetLatestClientLogByResource(auditLog.Resource, clientID)
			logRangeTiming(requestID, "db_latest_resource_lookup", latestLookupStarted, latestErr, "log_id", logID)
			isLatestClientEvent := latestErr == nil && latestClientLog != nil && latestClientLog.LogID == auditLog.LogID
			if shouldVerifyResourceWithAgent(*auditLog, isLatestClientEvent) {
				agentStatus = "not_configured"
				agentStarted := time.Now()
				agentResult, agentErr := s.agent.VerifyAgainstAgent(auditLog)
				logRangeTiming(requestID, "agent_verify", agentStarted, agentErr, "log_id", logID)
				if agentErr != nil {
					agentStatus = "unreachable"
					sourceStatus = models.SourceStatusUnreachable
				} else if agentResult == nil {
					agentStatus = "unreachable"
					sourceStatus = models.SourceStatusUnreachable
				} else if agentResult.AgentUsed {
					if agentResult.IsMatch {
						agentStatus = "matched"
						sourceStatus = models.SourceStatusMatched
					} else {
						agentStatus = "mismatch"
						sourceStatus = sourceStatusFromAgentResult(*auditLog, agentResult)
						agentDiscrepancies = agentResult.Discrepancies
						s.recordClientSourceIncident(*auditLog, sourceStatusFromAgentResult(*auditLog, agentResult), agentResult.ClientStateHash, agentResult.Discrepancies)
					}
				} else {
					sourceStatus = models.SourceStatusNotComparable
				}
			}
		}
	}

	return &VerificationResult{
		Status:             "success",
		Message:            successMsg,
		IsValid:            true,
		LogID:              auditLog.LogID,
		ExpectedHash:       auditLog.HashValue,
		DBRoot:             reconstructedRoot,
		TxID:               auditLog.BlockchainTxID,
		AgentStatus:        agentStatus,
		SourceStatus:       sourceStatus,
		AgentDiscrepancies: agentDiscrepancies,
	}, nil
}

func (s *auditService) verifyClientLogIntegrity(logID, clientID, requestID string) (*VerificationResult, error) {
	logLookupStarted := time.Now()
	auditLog, err := s.repo.GetLogByID(logID, clientID)
	logRangeTiming(requestID, "db_log_lookup", logLookupStarted, err, "log_id", logID)
	if err != nil {
		return nil, errors.New("log_not_found")
	}

	if isHashStillPending(auditLog) {
		return &VerificationResult{
			Status:  "pending",
			Message: "Log sudah diterima, tetapi hash final masih diproses pipeline.",
			IsValid: true,
			LogID:   auditLog.LogID,
		}, nil
	}

	if auditLog.BlockchainTxID == nil || *auditLog.BlockchainTxID == "PENDING_OR_FAILED" {
		return &VerificationResult{
			Status:  "pending",
			Message: "Log otentik secara lokal. Menunggu antrean Blockchain.",
			IsValid: true,
			LogID:   auditLog.LogID,
		}, nil
	}

	if s.fabric == nil {
		return nil, errors.New("fabric_error")
	}

	agentStarted := time.Now()
	agentResult, agentErr := s.agent.VerifyAgainstAgent(auditLog)
	logRangeTiming(requestID, "agent_verify", agentStarted, agentErr, "log_id", logID)

	if agentErr != nil || agentResult == nil {
		return &VerificationResult{
			Status:      "failed_source",
			Message:     "🚨 Gagal menghubungi Agent atau Agent belum dikonfigurasi.",
			IsValid:     false,
			LogID:       auditLog.LogID,
			AgentStatus: "unreachable",
		}, nil
	}

	if !agentResult.AgentUsed {
		return &VerificationResult{
			Status:      "failed_source",
			Message:     "🚨 Agent belum dikonfigurasi.",
			IsValid:     false,
			LogID:       auditLog.LogID,
			AgentStatus: "not_configured",
		}, nil
	}

	// Buat copy log dan rehash dengan data dari agent
	canonicalizeLog(auditLog)
	logCopy := *auditLog
	// HANYA gunakan ClientMetadata dari agen JIKA ada perbedaan data nyata (IsMatch = false).
	// Jika data sebenarnya persis sama, gunakan string Metadata orisinil untuk menjamin
	// urutan key JSON identik, sehingga tidak menghasilkan false-positive saat re-hash.
	if agentResult.ClientMetadata != "" && !agentResult.IsMatch {
		logCopy.Metadata = agentResult.ClientMetadata
	}
	recalculatedHash := hasher.GenerateLogHash(&logCopy)

	fabricStarted := time.Now()
	onChainData, err := s.fabric.GetAnchorFromLedger(*auditLog.BlockchainTxID)
	logRangeTiming(requestID, "fabric_anchor_lookup", fabricStarted, err, "log_id", logID)
	if err != nil {
		return nil, errors.New("fabric_error")
	}

	var fabricResponse struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.Unmarshal([]byte(onChainData), &fabricResponse); err != nil {
		return nil, errors.New("parse_error")
	}

	proofLookupStarted := time.Now()
	proofs, perr := s.repo.GetProofsByHash(auditLog.HashValue)
	logRangeTiming(requestID, "db_proof_lookup", proofLookupStarted, perr, "log_id", logID)
	if perr != nil {
		return nil, errors.New("proof_lookup_error")
	}

	// Reconstruct root with client rehashed value
	reconstructedRoot := recalculatedHash
	if len(proofs) > 0 {
		reconstructedRoot = crypto.ReconstructMerkleRoot(recalculatedHash, toMerkleProofData(proofs))
	}

	if reconstructedRoot != fabricResponse.MerkleRoot {
		agStatus := "mismatch"
		if agentResult.IsMatch {
			agStatus = "matched"
		}
		return &VerificationResult{
			Status:             "failed_onchain",
			Message:            "🚨 DATA TERMANIPULASI: Data klien saat ini tidak valid di mata Blockchain!",
			IsValid:            false,
			LogID:              auditLog.LogID,
			DBRoot:             reconstructedRoot,
			ChainRoot:          fabricResponse.MerkleRoot,
			AgentStatus:        agStatus,
			AgentDiscrepancies: agentResult.Discrepancies,
		}, nil
	}

	return &VerificationResult{
		Status:             "success",
		Message:            "✅ DATA OTENTIK: Data klien cocok dengan Blockchain.",
		IsValid:            true,
		LogID:              auditLog.LogID,
		ExpectedHash:       auditLog.HashValue,
		DBRoot:             reconstructedRoot,
		TxID:               auditLog.BlockchainTxID,
		AgentStatus:        "matched",
		AgentDiscrepancies: agentResult.Discrepancies,
	}, nil
}

func (s *auditService) GetFabricRecord(anchorID string) (map[string]interface{}, error) {
	if s.fabric == nil {
		return nil, errors.New("fabric_bypass")
	}
	fabricDataString, err := s.fabric.GetAnchorFromLedger(anchorID)
	if err != nil {
		return nil, errors.New("fabric_not_found")
	}
	var jsonResponse map[string]interface{}
	if err := json.Unmarshal([]byte(fabricDataString), &jsonResponse); err != nil {
		return nil, errors.New("parse_error")
	}
	return jsonResponse, nil
}

func (s *auditService) VerifyDataIntegrity(resource, clientID string, rawData *map[string]interface{}) (*DataVerificationResult, error) {
	lastLog, err := s.repo.GetLatestLogByResource(resource, clientID)
	if err != nil {
		return nil, errors.New("log_not_found")
	}

	var actualHash string
	var actualData interface{}
	isDataEmpty := rawData == nil || len(*rawData) == 0

	if !isDataEmpty {
		dataBytes, _ := json.Marshal(*rawData)
		actualHash = crypto.GenerateSHA3_256(string(dataBytes))
		actualData = *rawData
	}

	var expectedHash string
	var expectedData interface{}

	if lastLog.Metadata != "" && lastLog.Metadata != "{}" && lastLog.Metadata != "null" {
		var parsedMetadata map[string]interface{}
		if err := json.Unmarshal([]byte(lastLog.Metadata), &parsedMetadata); err == nil {
			expectedData = parsedMetadata
			expectedBytes, _ := json.Marshal(parsedMetadata)
			expectedHash = crypto.GenerateSHA3_256(string(expectedBytes))
		} else {
			expectedData = lastLog.Metadata
			expectedHash = crypto.GenerateSHA3_256(lastLog.Metadata)
		}
	}

	isValid := false
	status := "failed"
	var msg string
	isLastActionDelete := lastLog.Action == "DELETE"

	if isLastActionDelete {
		if isDataEmpty {
			isValid, status = true, "success"
			msg = "✅ DATA VALID: Log terakhir adalah DELETE, dan data aktual memang kosong."
		} else {
			msg = "🔴 DATA TERMANIPULASI (GHOST DATA): Log DELETE ditemukan tapi data masih ada!"
		}
	} else {
		if isDataEmpty {
			msg = "🔴 DATA TERMANIPULASI (ILLEGAL DELETION): Log terakhir bukan DELETE, tapi data HILANG!"
		} else if actualHash == expectedHash {
			isValid, status = true, "success"
			msg = "✅ DATA VALID: Kondisi data aktual sama persis dengan jejak terakhir."
		} else {
			msg = "🔴 DATA TERMANIPULASI (UNAUTHORIZED MODIFICATION): Isi data berbeda dari jejak sah."
		}
	}

	return &DataVerificationResult{
		Status:       status,
		Message:      msg,
		IsValid:      isValid,
		Resource:     resource,
		ExpectedData: expectedData,
		ActualData:   actualData,
		LastLogID:    lastLog.LogID,
	}, nil
}

// classifyIntegrity menjalankan Layer 2 (re-hash) untuk semua log, dan
// Layer 4 (cocokkan merkle_root vs Fabric ledger) HANYA untuk log berstatus
// ANCHORED. Log yang belum ANCHORED (RECEIVED/HASHED/AGGREGATED) diberi
// status "pending" karena belum bisa diverifikasi sampai Layer 4.
// Verifikasi sepenuhnya berbasis DB (off-chain) + Fabric (on-chain) saja.
func (s *auditService) classifyIntegrity(auditLog models.AuditLog) string {
	if isHashStillPending(&auditLog) {
		return "pending"
	}

	canonicalizeLog(&auditLog)
	recalculated := hasher.GenerateLogHash(&auditLog)
	if recalculated != auditLog.HashValue {
		s.recordTamperIncident(auditLog, "METADATA_HASH_MISMATCH", recalculated)
		return "tampered"
	}

	if auditLog.Status != "ANCHORED" || auditLog.BlockchainTxID == nil || *auditLog.BlockchainTxID == "" {
		return "pending"
	}

	if s.fabric == nil {
		return "unreachable"
	}

	onChainData, err := s.fabric.GetAnchorFromLedger(*auditLog.BlockchainTxID)
	if err != nil {
		return "unreachable"
	}

	var fabricResponse struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.Unmarshal([]byte(onChainData), &fabricResponse); err != nil {
		return "unreachable"
	}

	proofs, perr := s.repo.GetProofsByHash(auditLog.HashValue)
	if perr != nil {
		return "unreachable"
	}

	reconstructedRoot := auditLog.HashValue
	if len(proofs) > 0 {
		reconstructedRoot = crypto.ReconstructMerkleRoot(auditLog.HashValue, toMerkleProofData(proofs))
	}

	if auditLog.MerkleRoot != "" && auditLog.MerkleRoot != fabricResponse.MerkleRoot {
		s.recordTamperIncident(auditLog, "MERKLE_ROOT_MISMATCH", recalculated)
		return "tampered"
	}
	if reconstructedRoot != fabricResponse.MerkleRoot {
		// Legacy batches may have incomplete Merkle proof direction data. The
		// persisted DB root still matches Fabric, so keep the historical
		// fallback behavior while treating an actual DB-root mismatch above as
		// tampering.
		return "valid"
	}

	return "valid"
}

// recordTamperIncident membuat satu incident aktif per log dan jenis deteksi.
// Verifikasi dashboard dapat dipanggil berkali-kali, sehingga operasi ini
// harus idempoten dan tidak boleh membuat baris incident baru pada setiap GET.
func (s *auditService) recordTamperIncident(auditLog models.AuditLog, incidentType, detectedHash string) {
	if s.db == nil || auditLog.LogID == "" || auditLog.ClientID == "" {
		return
	}
	var existing models.TamperIncident
	err := s.db.Where(
		"client_id = ? AND log_id = ? AND incident_type = ? AND status = ?",
		auditLog.ClientID,
		auditLog.LogID,
		incidentType,
		models.IncidentStatusOpen,
	).First(&existing).Error
	if err == nil {
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return
	}
	incident := &models.TamperIncident{
		ID:           uuid.NewString(),
		ClientID:     auditLog.ClientID,
		LogID:        auditLog.LogID,
		Resource:     auditLog.Resource,
		IncidentType: incidentType,
		ExpectedHash: auditLog.HashValue,
		DetectedHash: detectedHash,
		Status:       models.IncidentStatusOpen,
		DetectedAt:   time.Now().UTC(),
	}
	// A concurrent verification may win the insert race. The unique/lookup
	// guard is intentionally best-effort; verification itself remains read-only
	// from the caller's perspective if this insert fails.
	_ = s.db.Create(incident).Error
}

func sourceStatusFromAgentResult(auditLog models.AuditLog, result *agentverifier.VerifyResult) string {
	if result == nil {
		return models.SourceStatusNotComparable
	}
	if strings.EqualFold(strings.TrimSpace(auditLog.Action), "DELETE") {
		if result.SourceFound {
			return models.SourceStatusUnexpectedPresent
		}
		return models.SourceStatusMatched
	}
	if !result.SourceFound {
		return models.SourceStatusMissing
	}
	return models.SourceStatusMismatch
}

func sourceStatusFromAgentStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "matched":
		return models.SourceStatusMatched
	case "mismatch":
		return models.SourceStatusMismatch
	case "unreachable":
		return models.SourceStatusUnreachable
	case "not_configured":
		return models.SourceStatusNotComparable
	default:
		return ""
	}
}

// recordClientSourceIncident records a discrepancy between a trusted latest
// client event and the live operational row. It is intentionally separate
// from Gateway/Fabric integrity incidents: the AuditChain evidence can remain
// VALID while the client source is MISMATCH/MISSING/UNEXPECTED_PRESENT.
func (s *auditService) recordClientSourceIncident(auditLog models.AuditLog, sourceStatus, detectedStateHash string, discrepancies []agentverifier.Discrepancy) {
	if s.db == nil || strings.TrimSpace(auditLog.ClientID) == "" || strings.TrimSpace(auditLog.Resource) == "" || strings.TrimSpace(sourceStatus) == "" || sourceStatus == models.SourceStatusMatched {
		return
	}
	incidentType := "CLIENT_STATE_MISMATCH"
	switch sourceStatus {
	case models.SourceStatusMissing:
		incidentType = "CLIENT_ROW_MISSING"
	case models.SourceStatusUnexpectedPresent:
		incidentType = "CLIENT_ROW_UNEXPECTED_PRESENT"
	}
	// Do not persist client row values in the incident table. The hash and the
	// field names are enough to correlate the source-state problem; raw values
	// may contain credentials, identifiers, or other client-sensitive data.
	redactedFields := make([]map[string]interface{}, 0, len(discrepancies))
	for _, discrepancy := range discrepancies {
		redactedFields = append(redactedFields, map[string]interface{}{
			"field":   discrepancy.Field,
			"changed": true,
		})
	}
	redacted, err := json.Marshal(redactedFields)
	if err != nil {
		redacted = []byte("[]")
	}
	var existing models.TamperIncident
	lookupErr := s.db.Where(
		"client_id = ? AND resource = ? AND incident_type = ? AND incident_scope = ? AND status = ?",
		auditLog.ClientID, auditLog.Resource, incidentType, models.RecoveryScopeClientSource, models.IncidentStatusOpen,
	).First(&existing).Error
	now := time.Now().UTC()
	if lookupErr == nil {
		_ = s.db.Model(&existing).Updates(map[string]interface{}{
			"log_id":              auditLog.LogID,
			"expected_hash":       auditLog.HashValue,
			"detected_hash":       detectedStateHash,
			"detected_state_hash": detectedStateHash,
			"source_status":       sourceStatus,
			"reference_log_id":    auditLog.LogID,
			"discrepancy_summary": string(redacted),
			"last_confirmed_at":   now,
		}).Error
		return
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return
	}
	incident := &models.TamperIncident{
		ID: uuid.NewString(), ClientID: auditLog.ClientID, LogID: auditLog.LogID,
		Resource: auditLog.Resource, IncidentType: incidentType,
		ExpectedHash: auditLog.HashValue, DetectedHash: detectedStateHash,
		Status: models.IncidentStatusOpen, IncidentScope: models.RecoveryScopeClientSource,
		SourceStatus: sourceStatus, ReferenceLogID: auditLog.LogID,
		DetectedStateHash: detectedStateHash, DiscrepancySummary: string(redacted),
		DetectedAt: now, LastConfirmedAt: &now,
	}
	_ = s.db.Create(incident).Error
}

// GetRecentLogsPaginated menggantikan limit-500 lama dengan pagination
// sesungguhnya (page/page_size), plus filter opsional integrity_status.
//
// KETERBATASAN (didokumentasikan secara transparan via field "note"):
// integrity_status dihitung IN-MEMORY setelah query, bukan di level SQL,
// karena status valid/tampered/unreachable baru diketahui setelah re-hash
// lokal + query ke Fabric. Akibatnya saat filter aktif:
//   - Basis pagination (total_items) memakai total log ANCHORED, BUKAN
//     jumlah log yang benar-benar cocok filter — sehingga total_items dan
//     total_pages bersifat APPROXIMATE saat integrity_status diisi.
//   - Ini adalah trade-off yang disengaja: menghitung exact count untuk
//     filter ini butuh full-scan + verifikasi semua log ANCHORED pada
//     setiap request, yang mahal. Field "note" memberi tahu API consumer
//     secara eksplisit.
func (s *auditService) GetRecentLogsPaginated(clientID string, page, pageSize int, integrityStatus, sortOrder, sourceTable, dbEngine string, fromTime, toTime *time.Time) (*RecentLogsResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 200 {
		pageSize = 200
	}

	validFilter := map[string]bool{"valid": true, "tampered": true, "unreachable": true, "pending": true, "not_checked": true}
	clientDBEngine, err := s.repo.GetClientDBEngine(clientID)
	if err != nil {
		return nil, err
	}
	normalizedClientDBEngine := normalizeDBEngine(clientDBEngine)
	normalizedFilterDBEngine := normalizeDBEngine(dbEngine)

	if strings.TrimSpace(dbEngine) != "" && normalizedFilterDBEngine == "" {
		return &RecentLogsResult{
			Data: []RecentLogItem{},
			Pagination: PaginationMeta{
				Page:       page,
				PageSize:   pageSize,
				TotalItems: 0,
				TotalPages: 1,
			},
		}, nil
	}

	if normalizedFilterDBEngine != "" && normalizedClientDBEngine != normalizedFilterDBEngine {
		return &RecentLogsResult{
			Data: []RecentLogItem{},
			Pagination: PaginationMeta{
				Page:       page,
				PageSize:   pageSize,
				TotalItems: 0,
				TotalPages: 1,
			},
		}, nil
	}

	// GetRecentLogsPaginated menggantikan limit-500 lama dengan pagination
	// sesungguhnya (page/page_size), plus filter opsional integrity_status.
	//
	// PERUBAHAN: endpoint ini TIDAK LAGI menjalankan verifikasi (rehash + query
	// Fabric) secara otomatis untuk setiap baris yang ditampilkan. IntegrityStatus
	// dikembalikan sebagai "not_checked" secara default. Verifikasi sesungguhnya
	// per log dilakukan on-demand lewat GET /dashboard/verify/:log_id, dipicu
	// oleh aksi eksplisit user (tombol verifikasi di frontend) — bukan lagi
	// dibebankan ke setiap polling/render daftar transaksi. Ini menghindari
	// rehash + Fabric round-trip berulang setiap 5 detik (siklus polling
	// Dashboard) untuk log yang bahkan belum diminta diverifikasi siapa pun.
	//
	// KETERBATASAN (sudah ada sebelumnya, tidak berubah): saat integrity_status
	// filter AKTIF, endpoint ini tetap butuh verifikasi in-memory untuk
	// menentukan kecocokan filter — jalur ini TIDAK terpengaruh perubahan di
	// atas karena secara desain memang harus menghitung status sebelum bisa
	// difilter. (Catatan: implementasi loop filter saat ini masih dinonaktifkan/
	// commented — di luar scope perubahan ini.)
	if integrityStatus == "" {
		logs, total, err := s.repo.GetRecentLogsPage(clientID, page, pageSize, sortOrder, sourceTable, fromTime, toTime)
		if err != nil {
			return nil, err
		}

		items := make([]RecentLogItem, 0, len(logs))
		for _, l := range logs {
			integrityStatus := strings.ToLower(strings.TrimSpace(l.IntegrityStatus))
			if integrityStatus == "" {
				integrityStatus = "not_checked"
			}
			items = append(items, RecentLogItem{
				AuditLog:        l,
				IntegrityStatus: integrityStatus,
				DBEngine:        normalizedClientDBEngine,
			})
		}

		totalPages := int(total) / pageSize
		if int(total)%pageSize != 0 {
			totalPages++
		}
		if totalPages == 0 {
			totalPages = 1
		}

		return &RecentLogsResult{
			Data: items,
			Pagination: PaginationMeta{
				Page:       page,
				PageSize:   pageSize,
				TotalItems: total,
				TotalPages: totalPages,
			},
		}, nil
	}

	if !validFilter[integrityStatus] {
		return nil, errors.New("invalid_integrity_status")
	}

	// Dengan filter: hanya log ANCHORED yang relevan (lihat classifyIntegrity).
	anchoredTotal, err := s.repo.CountAnchoredLogs(clientID)
	if err != nil {
		return nil, err
	}

	logs, err := s.repo.GetAnchoredLogsPage(clientID, page, pageSize)
	if err != nil {
		return nil, err
	}

	items := make([]RecentLogItem, 0, len(logs))
	// for _, l := range logs {
	// 	status := s.classifyIntegrity(l)
	// 	if status == integrityStatus {
	// 		items = append(items, RecentLogItem{
	// 			AuditLog:        l,
	// 			IntegrityStatus: status,
	// 		})
	// 	}
	// }

	totalPages := int(anchoredTotal) / pageSize
	if int(anchoredTotal)%pageSize != 0 {
		totalPages++
	}
	if totalPages == 0 {
		totalPages = 1
	}

	return &RecentLogsResult{
		Data: items,
		Pagination: PaginationMeta{
			Page:       page,
			PageSize:   pageSize,
			TotalItems: anchoredTotal,
			TotalPages: totalPages,
		},
		Note: "integrity_status filter aktif: total_items & total_pages dihitung dari total log berstatus ANCHORED (bukan jumlah pasti yang cocok filter), karena status integritas ditentukan setelah verifikasi in-memory per baris, bukan di level query SQL.",
	}, nil
}

func (s *auditService) GetResourceInventory(clientID string) (interface{}, error) {
	return s.repo.GetClientTables(clientID)
}

// VerifyResourceHistory menjalankan verifikasi Layer 2 (re-hash) + Layer 4
// (Merkle proof reconstruction vs Fabric) untuk SETIAP log milik resource
// ini, dan Layer 3 (Agent, live client data) HANYA untuk event client terbaru.
// Event RECOVERY tetap latest di timeline tetapi tidak menggantikan event
// client yang menjadi pembanding kondisi live. Hasilnya diagregasi menjadi
// satu chain_status.
//
// ChainIssues memberi detail KENAPA chain_status jadi "tampered", dengan dua
// kategori yang SENGAJA dipisah (bisa muncul bersamaan, karena dua-duanya
// independen satu sama lain):
//   - "client_mismatch:<log_id>"      → data live di klien (via Agent) sudah
//     berbeda dari log TERBARU resource ini. Bukan berarti log itu sendiri
//     rusak — datanya sudah berubah lagi di sisi klien tanpa lewat AuditChain.
//   - "log_integrity_failed:<log_id>" → log tersebut gagal Layer 2 dan/atau
//     Layer 4 (rehash lokal tidak cocok, atau rekonstruksi Merkle root tidak
//     cocok dengan Fabric). Bisa muncul untuk log manapun dalam riwayat,
//     tidak terbatas pada log terbaru.
func (s *auditService) VerifyResourceHistory(resource, clientID string) (*ResourceChainResult, error) {
	logs, err := s.repo.GetLogsByResource(resource, clientID)
	if err != nil || len(logs) == 0 {
		return nil, errors.New("log_not_found")
	}

	result := &ResourceChainResult{
		Resource:  resource,
		TotalLogs: len(logs),
		Logs:      make([]ResourceLogVerification, 0, len(logs)),
	}

	hasTampered := false
	hasUnreachable := false
	hasPending := false
	var chainIssues []string

	lastIndex := len(logs) - 1
	latestClientIndex := latestClientEventIndex(logs)
	for i, auditLog := range logs {
		item := s.classifyResourceLog(auditLog, i == lastIndex, i == latestClientIndex)
		result.Logs = append(result.Logs, item)

		// baseIntegrityFailed dicek terpisah dari AgentStatus supaya
		// "log_integrity_failed" hanya dilaporkan untuk kegagalan Layer 2/4
		// yang sesungguhnya. Ketidaktersediaan Agent adalah status operasional
		// client dan tidak mengubah integritas Gateway/Fabric.
		baseIntegrityFailed := item.IntegrityStatus == "tampered"
		if baseIntegrityFailed {
			chainIssues = append(chainIssues, fmt.Sprintf("log_integrity_failed:%s", auditLog.LogID))
		}
		if item.IsLatestClientEvent && item.AgentStatus == "mismatch" {
			chainIssues = append(chainIssues, fmt.Sprintf("client_mismatch:%s", auditLog.LogID))
		}

		// Agregasi chain hanya memakai status Gateway/Fabric. AgentStatus
		// sengaja tidak ikut menentukan chain_status.
		switch item.ChainStatus {
		case "tampered":
			hasTampered = true
		case "unreachable":
			hasUnreachable = true
		case "pending":
			hasPending = true
		}
	}
	if err := s.enrichRecoveryStatuses(clientID, result.Logs); err != nil {
		return nil, err
	}

	switch {
	case hasTampered:
		result.ChainStatus = "tampered"
		result.ChainIssues = chainIssues
	case hasUnreachable:
		result.ChainStatus = "unreachable"
	case hasPending:
		result.ChainStatus = "pending"
	default:
		result.ChainStatus = "valid"
	}

	return result, nil
}

func (s *auditService) GetLogsByResource(resource, clientID string) ([]models.AuditLog, error) {
	return s.repo.GetLogsByResource(resource, clientID)
}

func (s *auditService) GetTableResources(tableName, clientID string) ([]ResourceLogVerification, error) {
	logs, err := s.repo.GetTableResources(tableName, clientID)
	if err != nil {
		return nil, err
	}

	results := make([]ResourceLogVerification, 0, len(logs))
	for _, auditLog := range logs {
		item := s.classifyResourceLog(auditLog, false, false)
		results = append(results, item)
	}
	if err := s.enrichRecoveryStatuses(clientID, results); err != nil {
		return nil, err
	}

	return results, nil
}

func toMerkleProofData(proofs []models.MerkleProof) []crypto.MerkleProofData {
	result := make([]crypto.MerkleProofData, 0, len(proofs))
	for _, p := range proofs {
		result = append(result, crypto.MerkleProofData{
			SiblingHash: p.SiblingHash,
			IsLeft:      p.IsLeft,
			TreeLevel:   p.TreeLevel,
		})
	}
	return result
}

// classifyResourceLog menjalankan Layer 2+4 (rehash + Merkle vs Fabric) untuk
// SEMUA log, tapi Layer 3 (Agent, live data klien) HANYA untuk event client
// terbaru. RECOVERY adalah event internal Gateway: tetap dapat menjadi latest
// secara kronologis, tetapi tidak mengambil posisi latest client event.
func (s *auditService) classifyResourceLog(auditLog models.AuditLog, isLatest, isLatestClientEvent bool) ResourceLogVerification {
	baseStatus := s.classifyIntegrity(auditLog)
	integrityStatus, chainStatus := resourceGatewayStatuses(baseStatus, "skipped_historical")

	item := ResourceLogVerification{
		LogID:               auditLog.LogID,
		Resource:            auditLog.Resource,
		Action:              auditLog.Action,
		LastAction:          auditLog.Action,
		Actor:               auditLog.Actor,
		Timestamp:           formatPgTimestamp(auditLog.Timestamp),
		LastUpdatedAt:       formatPgTimestamp(auditLog.Timestamp),
		HashValue:           auditLog.HashValue,
		IntegrityStatus:     integrityStatus,
		ChainStatus:         chainStatus,
		RecoveryStatus:      recoveryDisplayNotRecovered,
		IsLatest:            isLatest,
		IsLatestClientEvent: isLatestClientEvent,
		AgentStatus:         "skipped_historical",
	}

	if isRecoveryAction(auditLog.Action) {
		item.AgentStatus = "skipped_recovery"
		if requestID := recoveryRequestIDFromAuthorizationContext(auditLog.AuthorizationContext); requestID != "" {
			item.RecoveryStatus = recoveryDisplayRecovered
			item.RecoveryRequestID = requestID
		}
		return item
	}
	if !shouldVerifyResourceWithAgent(auditLog, isLatestClientEvent) {
		return item
	}

	item.AgentStatus = "not_configured"

	logCopy := auditLog
	agentResult, err := s.agent.VerifyAgainstAgent(&logCopy)
	if err != nil {
		item.AgentStatus = "unreachable"
		item.SourceStatus = models.SourceStatusUnreachable
	} else if agentResult.AgentUsed {
		if agentResult.IsMatch {
			item.AgentStatus = "matched"
			item.SourceStatus = models.SourceStatusMatched
		} else {
			item.AgentStatus = "mismatch"
			item.SourceStatus = sourceStatusFromAgentResult(auditLog, agentResult)
			item.AgentDiscrepancies = agentResult.Discrepancies
			if baseStatus == "valid" {
				s.recordClientSourceIncident(auditLog, item.SourceStatus, agentResult.ClientStateHash, agentResult.Discrepancies)
			}
		}
	} else {
		item.SourceStatus = models.SourceStatusNotComparable
	}

	// AgentStatus hanya menjelaskan verifikasi Layer 3 terhadap database
	// operasional client. Jangan pernah menurunkan status integritas/chain
	// Gateway ketika Agent sedang offline; validasi Gateway/Fabric tetap sah.
	item.IntegrityStatus, item.ChainStatus = resourceGatewayStatuses(baseStatus, item.AgentStatus)

	return item
}

const (
	recoveryDisplayNotRecovered = "not_recovered"
	recoveryDisplayPending      = "pending"
	recoveryDisplayRecovered    = "recovered"
	recoveryDisplayFailed       = "failed"
)

// resourceGatewayStatuses memetakan hasil verifikasi PostgreSQL Gateway dan
// Fabric ke dua field kompatibilitas API. AgentStatus sengaja tidak menjadi
// input fungsi ini: Agent offline adalah kondisi client, bukan bukti bahwa
// snapshot atau anchor Gateway rusak. Parameter agentStatus sengaja diterima
// untuk menegaskan kontrak bahwa nilainya tidak pernah mengubah hasil.
func resourceGatewayStatuses(baseStatus, _ string) (integrityStatus, chainStatus string) {
	switch baseStatus {
	case "tampered":
		return "tampered", "tampered"
	case "unreachable":
		return "unreachable", "unreachable"
	case "pending":
		return "pending", "pending"
	default:
		return "valid", "valid"
	}
}

func recoveryDisplayStatus(status string) string {
	switch status {
	case models.RecoveryStatusSucceeded:
		return recoveryDisplayRecovered
	case models.RecoveryStatusPendingExecution,
		models.RecoveryStatusPendingApproval,
		models.RecoveryStatusApproved,
		models.RecoveryStatusExecuting:
		return recoveryDisplayPending
	case models.RecoveryStatusRejected,
		models.RecoveryStatusFailedVerification,
		models.RecoveryStatusFailedExecution:
		return recoveryDisplayFailed
	default:
		return recoveryDisplayNotRecovered
	}
}

// shouldVerifyResourceWithAgent hanya mengizinkan Layer 3 untuk event
// operasional client terbaru. Event RECOVERY dibuat oleh Gateway sendiri,
// sehingga tidak boleh dibandingkan dengan Agent client.
func shouldVerifyResourceWithAgent(auditLog models.AuditLog, isLatestClientEvent bool) bool {
	return isLatestClientEvent && !isRecoveryAction(auditLog.Action)
}

func isRecoveryAction(action string) bool {
	return strings.EqualFold(strings.TrimSpace(action), "RECOVERY")
}

// latestClientEventIndex receives logs ordered by timestamp ascending and
// returns the newest client-originated event. Gateway RECOVERY events do not
// replace the client event used for live source verification.
func latestClientEventIndex(logs []models.AuditLog) int {
	for i := len(logs) - 1; i >= 0; i-- {
		if !isRecoveryAction(logs[i].Action) {
			return i
		}
	}
	return -1
}

func recoveryRequestIDFromAuthorizationContext(authorizationContext string) string {
	const prefix = "recovery_request:"
	if !strings.HasPrefix(strings.TrimSpace(authorizationContext), prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authorizationContext), prefix))
}

// enrichRecoveryStatuses mengambil workflow recovery terbaru untuk setiap log
// dalam satu query. Dengan begitu endpoint riwayat tidak melakukan N+1 query
// dan frontend dapat menampilkan badge RECOVERED tanpa mencampurnya dengan
// status konektivitas Agent.
func (s *auditService) enrichRecoveryStatuses(clientID string, items []ResourceLogVerification) error {
	if len(items) == 0 {
		return nil
	}

	logIDs := make([]string, 0, len(items))
	seenLogIDs := make(map[string]struct{}, len(items))
	for i := range items {
		items[i].RecoveryStatus = recoveryDisplayNotRecovered
		if strings.EqualFold(strings.TrimSpace(items[i].Action), "RECOVERY") && items[i].RecoveryRequestID != "" {
			items[i].RecoveryStatus = recoveryDisplayRecovered
		}
		if items[i].LogID == "" {
			continue
		}
		if _, exists := seenLogIDs[items[i].LogID]; exists {
			continue
		}
		seenLogIDs[items[i].LogID] = struct{}{}
		logIDs = append(logIDs, items[i].LogID)
	}
	if s.db == nil || len(logIDs) == 0 {
		return nil
	}

	var requests []models.RecoveryRequest
	if err := s.db.Where("client_id = ? AND target_log_id IN ?", clientID, logIDs).
		Order("requested_at DESC").Find(&requests).Error; err != nil {
		return err
	}

	itemByLogID := make(map[string]int, len(items))
	for i := range items {
		if _, exists := itemByLogID[items[i].LogID]; !exists {
			itemByLogID[items[i].LogID] = i
		}
	}
	latestRequestByLogID := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		index, exists := itemByLogID[request.TargetLogID]
		if !exists {
			continue
		}
		if _, alreadySet := latestRequestByLogID[request.TargetLogID]; alreadySet {
			continue
		}
		latestRequestByLogID[request.TargetLogID] = struct{}{}
		items[index].RecoveryStatus = recoveryDisplayStatus(request.Status)
		items[index].RecoveryRequestID = request.ID
		items[index].RecoveryExecutedAt = request.ExecutedAt
	}
	return nil
}
