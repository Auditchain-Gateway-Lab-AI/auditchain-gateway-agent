package aggregator

import (
	"fmt"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/crypto"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Engine struct {
	DB             *gorm.DB
	RecoveryCutoff *time.Time
	// SnapshotRequired is an optional deployment-level override. A nil value
	// preserves the legacy environment behaviour for callers that construct an
	// Engine directly. The direct client-DB recovery mode sets this explicitly
	// to false so the normal client CDC pipeline can still hash/anchor events
	// when MinIO is intentionally disabled.
	SnapshotRequired *bool
}

func (a *Engine) snapshotRequired() bool {
	if a != nil && a.SnapshotRequired != nil {
		return *a.SnapshotRequired
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("SNAPSHOT_REQUIRED_FOR_ANCHOR")), "true")
}

// ProcessBatch mengelompokkan log transaksi yang sudah di-hash dan membuat Merkle Root
func (a *Engine) ProcessBatch(batchSize int) error {
	var logs []models.AuditLog

	// 1. Ambil log yang siap diagregasi (maksimal sejumlah batchSize).
	// Saat snapshot wajib diaktifkan, log tanpa snapshot tervalidasi tidak
	// boleh masuk Merkle/Fabric karena tidak dapat dipulihkan kembali.
	// RECOVERY is an internal Gateway event. New recovery evidence lives in
	// recovery_events and must not be reintroduced into the client audit tree;
	// legacy RECOVERY rows remain queryable for forensics only.
	query := a.DB.Where("status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "HASHED")
	if a.RecoveryCutoff != nil {
		// Legacy rows predate the snapshot pipeline. They must not be
		// re-anchored by the new pipeline or keep the new-scope gate blocked.
		query = query.Where("db_timestamp IS NOT NULL AND db_timestamp >= ?", *a.RecoveryCutoff)
	}
	if a.snapshotRequired() {
		query = query.Where("snapshot_status = ?", models.SnapshotStatusVerified)
	}
	if err := query.Order("timestamp asc").Limit(batchSize).Find(&logs).Error; err != nil {
		return err
	}

	if len(logs) == 0 {
		return nil // Tidak ada data untuk diproses
	}

	var hashes []string
	for _, l := range logs {
		hashes = append(hashes, l.HashValue)
	}

	// 2. Bangun Merkle Tree dan dapatkan Root beserta Proof-nya
	merkleResult := crypto.BuildMerkleTree(hashes)
	if merkleResult == nil {
		return nil
	}

	// 3. Gunakan Database Transaction agar semua proses update aman dan atomik
	err := a.DB.Transaction(func(tx *gorm.DB) error {

		// A. Simpan Merkle Metadata (Aktivitas 8) [cite: 240-242]
		merkleMeta := models.MerkleMetadata{
			MerkleRoot: merkleResult.Root,
			BatchSize:  len(logs),
		}
		if err := tx.Create(&merkleMeta).Error; err != nil {
			return err
		}

		// B. Update Audit Logs dan Simpan Merkle Proofs
		for _, logItem := range logs {
			// Update status log menjadi siap dikirim ke blockchain
			logItem.MerkleRoot = merkleResult.Root
			logItem.Status = "AGGREGATED"
			// Jangan memakai Save(&logItem) di sini. Snapshot worker dapat
			// menyelesaikan outbox secara bersamaan setelah batch ini dibaca;
			// Save akan menulis seluruh struct lama dan berpotensi mengembalikan
			// snapshot_status/object_key/version_id ke nilai stale (PENDING/kosong)
			// walaupun outbox sudah COMPLETED. Update field yang memang dimiliki
			// aggregator saja agar referensi snapshot tidak pernah tertimpa.
			result := tx.Model(&models.AuditLog{}).
				Where("log_id = ? AND status = ?", logItem.LogID, "HASHED").
				Updates(map[string]interface{}{
					"merkle_root": merkleResult.Root,
					"status":      "AGGREGATED",
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("audit log %s tidak lagi berstatus HASHED saat agregasi", logItem.LogID)
			}

			// Ambil dan simpan bukti sibling (Merkle Proof) ke database
			proofs := merkleResult.Proofs[logItem.HashValue]
			for _, p := range proofs {
				mp := models.MerkleProof{
					TransactionHash: logItem.HashValue,
					SiblingHash:     p.SiblingHash,
					IsLeft:          p.IsLeft, // BARU — sebelumnya hilang, membuat proof tidak bisa direkonstruksi dengan urutan yang benar
					TreeLevel:       p.TreeLevel,
					MerkleRoot:      merkleResult.Root,
				}
				if err := tx.Create(&mp).Error; err != nil {
					return err
				}
			}
		}

		return nil
	})

	if err != nil {
		log.Printf("[Aggregator] ❌ Gagal menyimpan Merkle Batch: %v", err)
		return err
	}

	log.Printf("[Aggregator] ✅ Batch %d transaksi sukses diagregasi. Merkle Root: %s", len(logs), merkleResult.Root)
	return nil
}

// ProcessRecoveryBatch anchors recovery execution evidence independently from
// client audit logs. Legacy events still require a verified snapshot; direct
// client-DB events deliberately have no snapshot and are eligible once their
// immutable Agent readback evidence has been recorded.
func (a *Engine) ProcessRecoveryBatch(batchSize int) error {
	var events []models.RecoveryEvent
	if err := a.DB.Where("pipeline_status = ? AND (snapshot_status = ? OR (source_snapshot_object_key = '' AND cdc_status IN ?))", models.RecoveryPipelineHashed, models.SnapshotStatusVerified, []string{models.CDCStatusConfirmed, models.CDCStatusConflict, models.CDCStatusTimeout}).
		Order("executed_at asc").Limit(batchSize).Find(&events).Error; err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(events))
	for _, event := range events {
		hashes = append(hashes, event.EventHash)
	}
	merkleResult := crypto.BuildMerkleTree(hashes)
	if merkleResult == nil {
		return nil
	}
	err := a.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&models.MerkleMetadata{MerkleRoot: merkleResult.Root, BatchSize: len(events)}).Error; err != nil {
			return err
		}
		for _, event := range events {
			result := tx.Model(&models.RecoveryEvent{}).
				Where("id = ? AND pipeline_status = ?", event.ID, models.RecoveryPipelineHashed).
				Updates(map[string]interface{}{
					"merkle_root":     merkleResult.Root,
					"pipeline_status": models.RecoveryPipelineAggregated,
				})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("recovery event %s tidak lagi berstatus HASHED", event.ID)
			}
			for _, proof := range merkleResult.Proofs[event.EventHash] {
				if err := tx.Create(&models.MerkleProof{
					TransactionHash: event.EventHash,
					SiblingHash:     proof.SiblingHash,
					IsLeft:          proof.IsLeft,
					TreeLevel:       proof.TreeLevel,
					MerkleRoot:      merkleResult.Root,
				}).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		log.Printf("[Aggregator] recovery event batch gagal: %v", err)
		return err
	}
	log.Printf("[Aggregator] recovery event batch %d sukses diagregasi. Merkle Root: %s", len(events), merkleResult.Root)
	return nil
}
