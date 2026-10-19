package audit

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"auditchain-agent/internal/recovery"
	"auditchain-agent/internal/verify"
)

// ResourceReader queries raw operational data from the client database.
type ResourceReader interface {
	QueryResource(ctx context.Context, tableName, resourceID string) (verify.ResourceRecord, error)
}

// RecoveryExecutor can apply clean state to the client database.
type RecoveryExecutor interface {
	RecoverRow(ctx context.Context, tableName, recordID string, data map[string]interface{}) (*recovery.RecoverResult, error)
}

// ServiceConfig holds parameters for the audit service.
type ServiceConfig struct {
	ClientID    string
	AutoRecover bool
}

// Service orchestrates the audit and verification workflows.
type Service struct {
	cfg              ServiceConfig
	gateway          *GatewayClient
	reader           ResourceReader
	recoveryExecutor RecoveryExecutor
}

// NewService creates a new audit Service instance.
func NewService(cfg ServiceConfig, gateway *GatewayClient, reader ResourceReader, recoveryExecutor RecoveryExecutor) *Service {
	return &Service{
		cfg:              cfg,
		gateway:          gateway,
		reader:           reader,
		recoveryExecutor: recoveryExecutor,
	}
}

// FilterLatestLogs filters a list of offchain logs to keep only the newest/latest
// log for each resource. This prevents false-positive tamper alerts when comparing
// historical superseded logs with the current live database state.
func FilterLatestLogs(logs []OffchainLog) []OffchainLog {
	if len(logs) == 0 {
		return nil
	}

	// 1. Cek apakah ada log dengan IsLatest == true
	hasIsLatestFlag := false
	for _, l := range logs {
		if l.IsLatest {
			hasIsLatestFlag = true
			break
		}
	}

	resourceMap := make(map[string]OffchainLog)
	for _, l := range logs {
		// Jika flag IsLatest ada di dataset, kita utamakan log dengan IsLatest == true
		if hasIsLatestFlag && !l.IsLatest {
			continue
		}

		existing, found := resourceMap[l.Resource]
		if !found || l.Timestamp.After(existing.Timestamp) {
			resourceMap[l.Resource] = l
		}
	}

	filtered := make([]OffchainLog, 0, len(resourceMap))
	seen := make(map[string]bool)
	for _, l := range logs {
		if best, ok := resourceMap[l.Resource]; ok && best.LogID == l.LogID && !seen[l.Resource] {
			seen[l.Resource] = true
			filtered = append(filtered, best)
		}
	}

	return filtered
}

// AuditTimeRange audits all offchain logs between from and to.
// It automatically filters to only the latest log per resource.
func (s *Service) AuditTimeRange(ctx context.Context, from, to time.Time, autoRecover bool) (*Report, error) {
	start := time.Now()

	logs, err := s.gateway.GetOffchainLogs(ctx, from, to)
	if err != nil {
		return nil, fmt.Errorf("fetch offchain logs: %w", err)
	}

	latestLogs := FilterLatestLogs(logs)

	report := &Report{
		From:              from,
		To:                to,
		TotalLogs:         len(logs),
		AuditedLogs:       len(latestLogs),
		HistoricalSkipped: len(logs) - len(latestLogs),
		Results:           make([]LogResult, 0, len(latestLogs)),
	}

	for _, l := range latestLogs {
		result := s.AuditSingleLog(ctx, l, autoRecover)
		report.Results = append(report.Results, result)

		// Aggregate data integrity status
		switch result.DataStatus {
		case "VALID":
			report.DataValid++
		case "TAMPERED":
			report.DataTampered++
		case "NOT_FOUND":
			report.DataNotFound++
		default:
			report.DataError++
		}

		// Aggregate blockchain status
		switch result.BlockchainStatus {
		case "VALID":
			report.BlockchainValid++
		case "TAMPERED":
			report.BlockchainTampered++
		case "PENDING":
			report.BlockchainPending++
		case "SKIPPED":
			report.BlockchainSkipped++
		default:
			report.BlockchainError++
		}

		if result.AutoRecovered {
			report.AutoRecoveredCount++
		}
	}

	report.Duration = time.Since(start).String()
	return report, nil
}

// AuditResource audits the single latest offchain log for a given resource.
func (s *Service) AuditResource(ctx context.Context, resource string, autoRecover bool) (*LogResult, error) {
	offchainLog, err := s.gateway.GetLatestOffchainLog(ctx, resource)
	if err != nil {
		return nil, fmt.Errorf("fetch latest offchain log: %w", err)
	}
	if offchainLog == nil {
		return &LogResult{
			Resource:         resource,
			DataStatus:       "NOT_FOUND",
			BlockchainStatus: "SKIPPED",
			Error:            "no offchain log found for resource",
		}, nil
	}

	res := s.AuditSingleLog(ctx, *offchainLog, autoRecover)
	return &res, nil
}

// AuditSingleLog executes the audit sequence for a single offchain log.
//
// Sequence:
//  1. Select client data
//  2. Normalization & Canonicalization
//  3. Hash normalized client data
//  4. Get offchain hash value & Compare Hash
//  5. If valid, send hash value via Verify merkle root API to reconstruct & compare
//     If invalid, skip Merkle Root verification and optionally trigger recovery.
func (s *Service) AuditSingleLog(ctx context.Context, logEntry OffchainLog, autoRecover bool) LogResult {
	result := LogResult{
		LogID:          logEntry.LogID,
		Resource:       logEntry.Resource,
		Action:         logEntry.Action,
		MerkleRoot:     logEntry.MerkleRoot,
		BlockchainTxID: "",
	}
	if logEntry.BlockchainTxID != nil {
		result.BlockchainTxID = *logEntry.BlockchainTxID
	}

	// 1. Parse resource (TABLE:ID)
	tableName, recordID, err := parseResource(logEntry.Resource)
	if err != nil {
		result.DataStatus = "ERROR"
		result.BlockchainStatus = "SKIPPED"
		result.Error = fmt.Sprintf("invalid resource %q: %v", logEntry.Resource, err)
		return result
	}

	// 2. Select client data from local Oracle database
	var clientData map[string]interface{}
	clientFound := false
	if s.reader != nil {
		rec, err := s.reader.QueryResource(ctx, tableName, recordID)
		if err != nil {
			result.DataStatus = "ERROR"
			result.BlockchainStatus = "SKIPPED"
			result.Error = fmt.Sprintf("query client resource: %v", err)
			return result
		}
		clientFound = rec.Found
		clientData = rec.Data
	}

	// 3. Normalization, Canonicalization, and Hash Comparison
	clientID := logEntry.ClientID
	if clientID == "" {
		clientID = s.cfg.ClientID
	}

	clientHash, offchainHash, isMatch, discrepancies, err := CompareState(
		clientID,
		logEntry.Resource,
		logEntry.Action,
		clientData,
		clientFound,
		logEntry.Metadata,
	)
	result.ClientHash = clientHash
	result.OffchainHash = offchainHash
	result.Discrepancies = discrepancies

	if err != nil {
		result.DataStatus = "ERROR"
		result.BlockchainStatus = "SKIPPED"
		result.Error = fmt.Sprintf("compare state: %v", err)
		return result
	}

	// 4. Evaluate Hash Match
	if !isMatch {
		result.DataStatus = "TAMPERED"
		result.BlockchainStatus = "SKIPPED" // Do not compare Merkle root if local data is tampered

		// Optional Recovery
		if (autoRecover || s.cfg.AutoRecover) && s.recoveryExecutor != nil {
			recovered, recErr := s.triggerRecovery(ctx, tableName, recordID, logEntry)
			result.AutoRecovered = recovered
			if recErr != nil {
				result.RecoveryError = recErr.Error()
			}
		}
		return result
	}

	result.DataStatus = "VALID"

	// 5. Compare Merkle Root against Blockchain (only when client hash is valid)
	if logEntry.MerkleRoot == "" || logEntry.BlockchainTxID == nil || *logEntry.BlockchainTxID == "" {
		result.BlockchainStatus = "PENDING"
		return result
	}

	merkleResp, err := s.gateway.VerifyMerkleRoot(ctx, logEntry.MerkleRoot, *logEntry.BlockchainTxID)
	if err != nil {
		result.BlockchainStatus = "ERROR"
		result.Error = fmt.Sprintf("verify merkle root: %v", err)
		return result
	}

	if merkleResp.IsMatch {
		result.BlockchainStatus = "VALID"
	} else {
		result.BlockchainStatus = "TAMPERED"
	}

	return result
}

func (s *Service) triggerRecovery(ctx context.Context, tableName, recordID string, logEntry OffchainLog) (bool, error) {
	desiredState, err := ExtractOffchainState(logEntry.Metadata)
	if err != nil {
		return false, fmt.Errorf("extract clean state for recovery: %w", err)
	}

	log.Printf("[AuditService] Memulai auto-recovery untuk %s:%s", tableName, recordID)
	res, err := s.recoveryExecutor.RecoverRow(ctx, tableName, recordID, desiredState)
	if err != nil {
		log.Printf("[AuditService] Gagal auto-recovery %s:%s: %v", tableName, recordID, err)
		return false, err
	}

	log.Printf("[AuditService] Auto-recovery sukses untuk %s:%s (op=%s, cols=%v)", tableName, recordID, res.Operation, res.RecoveredColumns)
	return true, nil
}

func parseResource(resource string) (tableName, recordID string, err error) {
	resource = strings.TrimSpace(resource)
	var parts []string
	if strings.Contains(resource, ":") {
		parts = strings.SplitN(resource, ":", 2)
	} else if strings.Contains(resource, "/") {
		parts = strings.SplitN(resource, "/", 2)
	} else {
		return "", "", fmt.Errorf("format resource harus TABLE:ID")
	}

	tableName = strings.TrimSpace(parts[0])
	recordID = strings.TrimSpace(parts[1])
	if tableName == "" || recordID == "" {
		return "", "", fmt.Errorf("nama tabel atau record ID kosong")
	}
	return tableName, recordID, nil
}
