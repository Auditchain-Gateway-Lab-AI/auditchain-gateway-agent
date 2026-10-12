package crypto

import (
	"encoding/hex"

	"golang.org/x/crypto/sha3"
)

// MerkleProofData menyimpan informasi sibling hash untuk validasi
type MerkleProofData struct {
	SiblingHash string
	IsLeft      bool // true = sibling ada di kiri, false = sibling ada di kanan
	TreeLevel   int
}

// MerkleResult menyimpan output akhir agregasi
type MerkleResult struct {
	Root   string
	Proofs map[string][]MerkleProofData
}

// hashNodes menggabungkan dua hash menjadi parent menggunakan SHA3-256
func hashNodes(left, right string) string {
	leftBytes, _ := hex.DecodeString(left)
	rightBytes, _ := hex.DecodeString(right)
	combined := append(leftBytes, rightBytes...)
	hash := sha3.Sum256(combined)
	return hex.EncodeToString(hash[:])
}

// BuildMerkleTree membangun pohon secara berpasangan dan menghasilkan Root.
//
// FIX (sebelumnya): proof sibling hanya disimpan untuk levelIndex == 0,
// sehingga VerifyMerkleProof hanya benar untuk batch <= 2 leaf. Sekarang
// setiap level melacak "grup leaf" yang berada di bawah tiap node
// (currentGroups), sehingga proof chain lengkap tersimpan dari leaf sampai
// root untuk BATCH SEBERAPA PUN BESARNYA.
//
// Kasus leaf ganjil (node terakhir "dipasangkan dengan dirinya sendiri")
// juga sekarang direkam sebagai proof step (self-pairing), bukan dilewati
// begitu saja — tanpa ini, reconstruction untuk leaf di bawah node ganjil
// akan menghasilkan root yang salah.
func BuildMerkleTree(leafHashes []string) *MerkleResult {
	if len(leafHashes) == 0 {
		return nil
	}

	proofs := make(map[string][]MerkleProofData, len(leafHashes))
	for _, h := range leafHashes {
		proofs[h] = []MerkleProofData{}
	}

	currentLevel := make([]string, len(leafHashes))
	copy(currentLevel, leafHashes)

	// currentGroups[i] = daftar leaf hash asli yang berada di bawah node i
	// pada level saat ini. Dipakai untuk tahu leaf mana saja yang perlu
	// menerima proof step baru ketika dua node digabung.
	currentGroups := make([][]string, len(leafHashes))
	for i, h := range leafHashes {
		currentGroups[i] = []string{h}
	}

	levelIndex := 0

	for len(currentLevel) > 1 {
		var nextLevel []string
		var nextGroups [][]string

		for i := 0; i < len(currentLevel); i += 2 {
			leftHash := currentLevel[i]
			leftGroup := currentGroups[i]

			var rightHash string
			var rightGroup []string
			isDuplicate := false

			if i+1 == len(currentLevel) {
				// Leaf/node ganjil di ujung — dipasangkan dengan dirinya sendiri.
				rightHash = leftHash
				rightGroup = leftGroup
				isDuplicate = true
			} else {
				rightHash = currentLevel[i+1]
				rightGroup = currentGroups[i+1]
			}

			if isDuplicate {
				// Setiap leaf di bawah node ini perlu proof step "self-pairing":
				// current_hash = hash(current_hash, current_hash). Sibling-nya
				// adalah leftHash itu sendiri, karena pada titik ini
				// reconstructed-hash leaf tersebut sama dengan leftHash.
				for _, leaf := range leftGroup {
					proofs[leaf] = append(proofs[leaf], MerkleProofData{
						SiblingHash: leftHash,
						IsLeft:      false,
						TreeLevel:   levelIndex,
					})
				}
			} else {
				for _, leaf := range leftGroup {
					proofs[leaf] = append(proofs[leaf], MerkleProofData{
						SiblingHash: rightHash,
						IsLeft:      false,
						TreeLevel:   levelIndex,
					})
				}
				for _, leaf := range rightGroup {
					proofs[leaf] = append(proofs[leaf], MerkleProofData{
						SiblingHash: leftHash,
						IsLeft:      true,
						TreeLevel:   levelIndex,
					})
				}
			}

			parentHash := hashNodes(leftHash, rightHash)

			parentGroup := leftGroup
			if !isDuplicate {
				parentGroup = append(append([]string{}, leftGroup...), rightGroup...)
			}

			nextLevel = append(nextLevel, parentHash)
			nextGroups = append(nextGroups, parentGroup)
		}

		currentLevel = nextLevel
		currentGroups = nextGroups
		levelIndex++
	}

	return &MerkleResult{
		Root:   currentLevel[0],
		Proofs: proofs,
	}
}

// ReconstructMerkleRoot me-replay leaf hash melalui proof chain-nya untuk
// menurunkan root hash. Ini primitive inti yang dipakai baik oleh
// VerifyMerkleProof (bool check) maupun oleh service layer yang butuh
// root hasil rekonstruksi eksplisit (untuk dibandingkan/dicatat).
func ReconstructMerkleRoot(leafHash string, proofs []MerkleProofData) string {
	currentHash := leafHash
	for _, p := range proofs {
		if p.IsLeft {
			// Sibling ada di kiri: hash(sibling, current)
			currentHash = hashNodes(p.SiblingHash, currentHash)
		} else {
			// Sibling ada di kanan (atau self-pairing): hash(current, sibling)
			currentHash = hashNodes(currentHash, p.SiblingHash)
		}
	}
	return currentHash
}

// VerifyMerkleProof merekonstruksi hash dari leaf ke root dan membandingkannya
// dengan expectedRoot.
func VerifyMerkleProof(transactionHash string, proofs []MerkleProofData, expectedRoot string) bool {
	return ReconstructMerkleRoot(transactionHash, proofs) == expectedRoot
}
