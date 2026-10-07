package recovery

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/pkg/canonicalstate"
	"go-blockchain-api/pkg/crypto"
)

// TrustedReference is the immutable reference that a future Agent write is
// allowed to consume. It deliberately contains the AuditChain payload and its
// proof, rather than any MinIO object reference.
type TrustedReference struct {
	Log          models.AuditLog
	LeafHash     string
	MerkleRoot   string
	FabricRoot   string
	AnchorID     string
	Proofs       []models.MerkleProof
	CanonicalRaw []byte
}

// ValidateTrustedReference proves that an AuditChain log is safe to use as a
// recovery source. A root field copied in PostgreSQL is not sufficient: the
// stored leaf must be recomputed, the proof must reconstruct the root, and the
// same root must be read from Fabric.
func ValidateTrustedReference(logRow *models.AuditLog, proofs []models.MerkleProof, fabric FabricReader) (*TrustedReference, error) {
	if logRow == nil {
		return nil, errors.New("reference_log_missing")
	}
	if strings.EqualFold(strings.TrimSpace(logRow.Action), "RECOVERY") {
		return nil, errors.New("reference_internal_event_not_allowed")
	}
	if !strings.EqualFold(strings.TrimSpace(logRow.Status), "ANCHORED") {
		return nil, errors.New("reference_not_anchored")
	}
	if strings.TrimSpace(logRow.HashValue) == "" {
		return nil, errors.New("reference_hash_missing")
	}
	if !isSHA3Hash(logRow.HashValue) {
		return nil, errors.New("reference_hash_invalid")
	}
	if strings.TrimSpace(logRow.MerkleRoot) == "" {
		return nil, errors.New("reference_merkle_root_missing")
	}
	if !isSHA3Hash(logRow.MerkleRoot) {
		return nil, errors.New("reference_merkle_root_invalid")
	}
	if logRow.BlockchainTxID == nil || strings.TrimSpace(*logRow.BlockchainTxID) == "" {
		return nil, errors.New("reference_anchor_missing")
	}
	if fabric == nil {
		return nil, errors.New("fabric_unavailable")
	}

	canonicalMetadata, err := canonicalMetadata(logRow.Metadata)
	if err != nil {
		return nil, err
	}
	recomputedLeaf := hasher.GenerateLogHash(logRow)
	if !strings.EqualFold(recomputedLeaf, logRow.HashValue) {
		return nil, errors.New("reference_local_hash_mismatch")
	}

	orderedProofs, err := validateProofs(logRow.HashValue, logRow.MerkleRoot, proofs)
	if err != nil {
		return nil, err
	}
	reconstructedRoot := crypto.ReconstructMerkleRoot(logRow.HashValue, toMerkleProofData(orderedProofs))
	if !strings.EqualFold(reconstructedRoot, logRow.MerkleRoot) {
		return nil, errors.New("reference_merkle_proof_mismatch")
	}

	rawAnchor, err := fabric.GetAnchorFromLedger(strings.TrimSpace(*logRow.BlockchainTxID))
	if err != nil {
		return nil, fmt.Errorf("fabric_anchor_unreachable: %w", err)
	}
	fabricRoot, err := parseFabricRoot(rawAnchor)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(fabricRoot, logRow.MerkleRoot) || !strings.EqualFold(fabricRoot, reconstructedRoot) {
		return nil, errors.New("reference_fabric_root_mismatch")
	}

	return &TrustedReference{
		Log:          *logRow,
		LeafHash:     strings.ToLower(strings.TrimSpace(logRow.HashValue)),
		MerkleRoot:   strings.ToLower(strings.TrimSpace(logRow.MerkleRoot)),
		FabricRoot:   strings.ToLower(fabricRoot),
		AnchorID:     strings.TrimSpace(*logRow.BlockchainTxID),
		Proofs:       orderedProofs,
		CanonicalRaw: canonicalMetadata,
	}, nil
}

func canonicalMetadata(raw string) ([]byte, error) {
	if strings.TrimSpace(raw) == "" {
		// DELETE events may legitimately have no state payload. The audit leaf
		// still authenticates the empty metadata representation used by the
		// existing hasher.
		return nil, nil
	}
	canonical, err := canonicalstate.CanonicalizeJSON([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("reference_metadata_invalid: %w", err)
	}
	return canonical, nil
}

func validateProofs(leafHash, expectedRoot string, proofs []models.MerkleProof) ([]models.MerkleProof, error) {
	if len(proofs) == 0 {
		if !strings.EqualFold(leafHash, expectedRoot) {
			return nil, errors.New("reference_merkle_proof_missing")
		}
		return []models.MerkleProof{}, nil
	}

	ordered := append([]models.MerkleProof(nil), proofs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TreeLevel < ordered[j].TreeLevel })
	for index, proof := range ordered {
		if proof.TransactionHash != "" && !strings.EqualFold(strings.TrimSpace(proof.TransactionHash), leafHash) {
			return nil, errors.New("reference_proof_leaf_mismatch")
		}
		if proof.TreeLevel != index {
			return nil, errors.New("reference_proof_levels_invalid")
		}
		if !isSHA3Hash(proof.SiblingHash) || !isSHA3Hash(proof.MerkleRoot) {
			return nil, errors.New("reference_proof_hash_invalid")
		}
		if !strings.EqualFold(strings.TrimSpace(proof.MerkleRoot), expectedRoot) {
			return nil, errors.New("reference_proof_root_mismatch")
		}
	}
	return ordered, nil
}

func parseFabricRoot(raw string) (string, error) {
	var response struct {
		MerkleRoot string `json:"merkle_root"`
	}
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return "", fmt.Errorf("fabric_anchor_invalid: %w", err)
	}
	response.MerkleRoot = strings.ToLower(strings.TrimSpace(response.MerkleRoot))
	if !isSHA3Hash(response.MerkleRoot) {
		return "", errors.New("fabric_anchor_root_invalid")
	}
	return response.MerkleRoot, nil
}

func isSHA3Hash(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func toMerkleProofData(proofs []models.MerkleProof) []crypto.MerkleProofData {
	result := make([]crypto.MerkleProofData, len(proofs))
	for index, proof := range proofs {
		result[index] = crypto.MerkleProofData{
			SiblingHash: strings.ToLower(strings.TrimSpace(proof.SiblingHash)),
			IsLeft:      proof.IsLeft,
			TreeLevel:   proof.TreeLevel,
		}
	}
	return result
}
