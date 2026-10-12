package crypto

import (
	"encoding/hex"

	"golang.org/x/crypto/sha3"
)

// GenerateSHA3_256 menghasilkan hash SHA3-256 dari string input
func GenerateSHA3_256(data string) string {
	hash := sha3.Sum256([]byte(data))
	return hex.EncodeToString(hash[:])
}
