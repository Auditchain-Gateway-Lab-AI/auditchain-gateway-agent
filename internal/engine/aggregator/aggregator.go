package aggregator

import (
	"auditchain-agent/internal/backendsupport/crypto"
	"auditchain-agent/internal/models"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"
)

type Engine struct {
	DB             *gorm.DB
	RecoveryCutoff *time.Time
}

// ProcessBatch mengelompokkan log transaksi yang sudah di-hash dan membuat Merkle Root
func (a *Engine) ProcessBatch(batchSize int) error {
	var logs []models.AuditLog

	// 1. Ambil log yang siap diagregasi (maksimal sejumlah batchSize).
	// RECOVERY is an internal Gateway event. New recovery evidence lives in
	// recovery_events and must not be reintroduced into the client audit tree;
	// legacy RECOVERY rows remain queryable for forensics only.
	query := a.DB.Where("status = ? AND COALESCE(UPPER(TRIM(action)), '') <> 'RECOVERY'", "HASHED")
	if a.RecoveryCutoff != nil {
		// Legacy rows predate the snapshot pipeline. They must not be
		// re-anchored by the new pipeline or keep the new-scope gate blocked.
		query = query.Where("db_timestamp IS NOT NULL AND db_timestamp >= ?", *a.RecoveryCutoff)
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
