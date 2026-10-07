package recovery

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/crypto"
)

type trustedReferenceFabric struct {
	root string
	err  error
}

func (f trustedReferenceFabric) GetAnchorFromLedger(string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return `{"merkle_root":"` + f.root + `"}`, nil
}

func trustedReferenceLog() models.AuditLog {
	logRow := models.AuditLog{
		LogID:        "log-1",
		ClientID:     "client-1",
		Actor:        "POLINEMA",
		Action:       "UPDATE",
		Resource:     "RUANGAN:81",
		Timestamp:    time.Date(2026, 9, 29, 1, 2, 3, 4000, time.UTC),
		SourceSystem: "SIMRS Morbis 1",
		Metadata:     `{"ID":81,"NAMA":"ruang"}`,
		Status:       "ANCHORED",
	}
	logRow.HashValue = cryptoHashLog(&logRow)
	logRow.MerkleRoot = logRow.HashValue
	anchorID := "anchor-1"
	logRow.BlockchainTxID = &anchorID
	return logRow
}

func cryptoHashLog(logRow *models.AuditLog) string {
	return hasher.GenerateLogHash(logRow)
}

func TestValidateTrustedReferenceAcceptsSingleLeaf(t *testing.T) {
	logRow := trustedReferenceLog()
	if logRow.HashValue == "" {
		t.Fatal("test log hash was not initialized")
	}
	trusted, err := ValidateTrustedReference(&logRow, nil, trustedReferenceFabric{root: logRow.HashValue})
	if err != nil {
		t.Fatalf("valid reference rejected: %v", err)
	}
	if trusted.LeafHash != logRow.HashValue || trusted.FabricRoot != logRow.HashValue {
		t.Fatalf("trusted reference = %+v", trusted)
	}
}

func TestValidateTrustedReferenceAcceptsOrderedMultiLeafProof(t *testing.T) {
	first := trustedReferenceLog()
	second := first
	second.LogID = "log-2"
	second.Metadata = `{"ID":81,"NAMA":"ruang-updated"}`
	second.HashValue = hasher.GenerateLogHash(&second)
	merkle := crypto.BuildMerkleTree([]string{first.HashValue, second.HashValue})
	first.MerkleRoot = merkle.Root
	proofs := make([]models.MerkleProof, 0, len(merkle.Proofs[first.HashValue]))
	for _, proof := range merkle.Proofs[first.HashValue] {
		proofs = append(proofs, models.MerkleProof{
			TransactionHash: first.HashValue,
			SiblingHash:     proof.SiblingHash,
			IsLeft:          proof.IsLeft,
			TreeLevel:       proof.TreeLevel,
			MerkleRoot:      merkle.Root,
		})
	}
	trusted, err := ValidateTrustedReference(&first, proofs, trustedReferenceFabric{root: merkle.Root})
	if err != nil {
		t.Fatalf("valid multi-leaf reference rejected: %v", err)
	}
	if trusted.MerkleRoot != merkle.Root || len(trusted.Proofs) != len(proofs) {
		t.Fatalf("trusted reference proof result = %+v", trusted)
	}
}

func TestValidateTrustedReferenceRejectsLocalHashTamper(t *testing.T) {
	logRow := trustedReferenceLog()
	logRow.Metadata = `{"ID":81,"NAMA":"tampered"}`
	_, err := ValidateTrustedReference(&logRow, nil, trustedReferenceFabric{root: logRow.HashValue})
	if err == nil || err.Error() != "reference_local_hash_mismatch" {
		t.Fatalf("error = %v, want reference_local_hash_mismatch", err)
	}
}

func TestValidateTrustedReferenceRejectsFabricMismatch(t *testing.T) {
	logRow := trustedReferenceLog()
	_, err := ValidateTrustedReference(&logRow, nil, trustedReferenceFabric{root: strings.Repeat("a", 64)})
	if err == nil || err.Error() != "reference_fabric_root_mismatch" {
		t.Fatalf("error = %v, want reference_fabric_root_mismatch", err)
	}
}

func TestValidateTrustedReferenceRejectsFabricUnavailable(t *testing.T) {
	logRow := trustedReferenceLog()
	_, err := ValidateTrustedReference(&logRow, nil, trustedReferenceFabric{err: errors.New("offline")})
	if err == nil || !strings.HasPrefix(err.Error(), "fabric_anchor_unreachable:") {
		t.Fatalf("error = %v, want classified Fabric error", err)
	}
}

func TestValidateTrustedReferenceRejectsRecoveryEventAndInvalidProof(t *testing.T) {
	logRow := trustedReferenceLog()
	logRow.Action = "RECOVERY"
	if _, err := ValidateTrustedReference(&logRow, nil, trustedReferenceFabric{root: logRow.HashValue}); err == nil || err.Error() != "reference_internal_event_not_allowed" {
		t.Fatalf("recovery action error = %v", err)
	}

	logRow = trustedReferenceLog()
	logRow.MerkleRoot = strings.Repeat("b", 64)
	proof := models.MerkleProof{
		TransactionHash: logRow.HashValue,
		SiblingHash:     strings.Repeat("c", 64),
		TreeLevel:       1,
		MerkleRoot:      logRow.MerkleRoot,
	}
	if _, err := ValidateTrustedReference(&logRow, []models.MerkleProof{proof}, trustedReferenceFabric{root: logRow.MerkleRoot}); err == nil || err.Error() != "reference_proof_levels_invalid" {
		t.Fatalf("invalid proof error = %v", err)
	}
}
