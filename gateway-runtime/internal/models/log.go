package models

import "time"

const (
	IntegrityStatusNotChecked  = "NOT_CHECKED"
	IntegrityStatusValid       = "VALID"
	IntegrityStatusTampered    = "TAMPERED"
	IntegrityStatusPending     = "PENDING"
	IntegrityStatusUnreachable = "UNREACHABLE"

	IntegritySourceTamperScanner   = "TAMPER_SCANNER"
	IntegritySourceManualRange     = "MANUAL_VERIFY_RANGE"
	IntegritySourceBackgroundRun   = "BACKGROUND_VERIFY_RUN"
	IntegritySourceScheduledRun    = "SCHEDULED_VERIFY_RUN"
	IntegritySourceManualSingleLog = "MANUAL_SINGLE_LOG"
	IntegritySourceRecovery        = "RECOVERY"
)

// AuditLog merepresentasikan struktur metadata log transaksi
type AuditLog struct {
	LogID    string `gorm:"primaryKey;type:varchar(100)" json:"log_id"`
	ClientID string `gorm:"type:varchar(36);not null;index" json:"client_id"`

	Actor                string     `gorm:"type:varchar(100);index" json:"actor"`
	Action               string     `gorm:"type:varchar(100)" json:"action"`
	Resource             string     `gorm:"type:varchar(255)" json:"resource"`
	Timestamp            time.Time  `gorm:"index" json:"timestamp"`
	DBTimestamp          *time.Time `gorm:"index;autoCreateTime" json:"db_timestamp"`
	SourceSystem         string     `gorm:"type:varchar(100);index" json:"source_system"`
	AuthorizationContext string     `gorm:"type:text" json:"authorization_context"`
	Metadata             string     `gorm:"type:jsonb" json:"metadata"`
	IsLatest             bool       `gorm:"default:false;index" json:"is_latest"`

	// BARU: ID baris di audit_trail DB klien.
	// Diisi dari field "audit_trail_id" pada payload Agent.
	// Kosong jika log dikirim langsung (bukan via Agent).
	// Digunakan Lapis 3 untuk meminta Agent memverifikasi baris ini.
	SourceRecordID string `gorm:"type:varchar(100);index" json:"source_record_id"`

	// Elemen Kriptografi & Blockchain
	HashValue           string     `gorm:"type:varchar(64);uniqueIndex" json:"hash_value"`
	PreviousHash        string     `gorm:"type:varchar(64)" json:"previous_hash"`
	MerkleRoot          string     `gorm:"type:varchar(64);index" json:"merkle_root"`
	BlockchainTxID      *string    `gorm:"type:varchar(100)" json:"blockchain_tx_id"`
	Status              string     `gorm:"type:varchar(20);default:'RECEIVED'" json:"status"`
	BlockchainTimestamp *time.Time `gorm:"index" json:"blockchain_timestamp"`

	// Recovery snapshot reference. The payload itself lives in MinIO; these
	// fields are only the operational index used to read an exact object version.
	SnapshotStatus        string     `gorm:"type:varchar(30);index;default:'PENDING'" json:"snapshot_status"`
	SnapshotObjectKey     string     `gorm:"type:text" json:"snapshot_object_key"`
	SnapshotVersionID     string     `gorm:"type:varchar(255)" json:"snapshot_version_id"`
	SnapshotChecksum      string     `gorm:"type:varchar(64)" json:"snapshot_checksum"`
	SnapshotPlaintextHash string     `gorm:"type:varchar(64)" json:"snapshot_plaintext_hash"`
	SnapshotStoredAt      *time.Time `json:"snapshot_stored_at,omitempty"`
	SnapshotVerifiedAt    *time.Time `json:"snapshot_verified_at,omitempty"`
	SnapshotLastError     string     `gorm:"type:text" json:"snapshot_last_error,omitempty"`

	// Integrity verification is an operational cache and is never part of the
	// canonical hash formula. Empty provenance means the result predates tracking.
	IntegrityStatus    string     `gorm:"type:varchar(20);index;default:'NOT_CHECKED'" json:"integrity_status"`
	IntegrityCheckedAt *time.Time `gorm:"index" json:"integrity_checked_at,omitempty"`
	IntegrityError     string     `gorm:"type:text" json:"integrity_error,omitempty"`
	IntegritySource    string     `gorm:"type:varchar(40);default:''" json:"integrity_source,omitempty"`
	IntegrityRunID     string     `gorm:"type:varchar(36);default:''" json:"integrity_run_id,omitempty"`
}

type MerkleMetadata struct {
	TreeID         uint      `gorm:"primaryKey"`
	MerkleRoot     string    `gorm:"type:varchar(64);uniqueIndex"`
	BatchTimestamp time.Time `gorm:"autoCreateTime"`
	BatchSize      int       `gorm:"type:int"`
}

type MerkleProof struct {
	ID              uint   `gorm:"primaryKey"`
	TransactionHash string `gorm:"type:varchar(64);index"`
	SiblingHash     string `gorm:"type:varchar(64)"`
	IsLeft          bool   `gorm:"default:false"`
	TreeLevel       int    `gorm:"type:int"`
	MerkleRoot      string `gorm:"type:varchar(64);index"`
}
