package snapshotstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

type Cipher struct {
	keyID string
	key   []byte
}

type encryptedEnvelope struct {
	EnvelopeVersion int    `json:"envelope_version"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	Nonce           string `json:"nonce"`
	Ciphertext      string `json:"ciphertext"`
}

func NewCipher(keyID string, key []byte) (*Cipher, error) {
	if keyID == "" {
		return nil, fmt.Errorf("key ID snapshot wajib diisi")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("key snapshot harus 32 byte untuk AES-256, diterima %d byte", len(key))
	}
	keyCopy := append([]byte(nil), key...)
	return &Cipher{keyID: keyID, key: keyCopy}, nil
}

func NewCipherFromEncodedKey(keyID, encoded string) (*Cipher, error) {
	if encoded == "" {
		return nil, fmt.Errorf("key snapshot kosong")
	}

	key, base64Err := base64.StdEncoding.DecodeString(encoded)
	if base64Err != nil || len(key) != 32 {
		hexKey, hexErr := hex.DecodeString(encoded)
		if hexErr != nil {
			if base64Err != nil {
				return nil, fmt.Errorf("key snapshot bukan base64 atau hex yang valid: %w", base64Err)
			}
			return nil, fmt.Errorf("key snapshot base64 harus menghasilkan 32 byte, sedangkan hex gagal: %w", hexErr)
		}
		key = hexKey
	}
	return NewCipher(keyID, key)
}

func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, fmt.Errorf("membuat AES cipher gagal: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("membuat AES-GCM gagal: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("membuat nonce snapshot gagal: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	envelope := encryptedEnvelope{
		EnvelopeVersion: 1,
		Algorithm:       "AES-256-GCM",
		KeyID:           c.keyID,
		Nonce:           base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:      base64.StdEncoding.EncodeToString(ciphertext),
	}
	return json.Marshal(envelope)
}

func (c *Cipher) Decrypt(encrypted []byte) ([]byte, error) {
	var envelope encryptedEnvelope
	if err := json.Unmarshal(encrypted, &envelope); err != nil {
		return nil, fmt.Errorf("envelope snapshot tidak valid: %w", err)
	}
	if envelope.EnvelopeVersion != 1 || envelope.Algorithm != "AES-256-GCM" {
		return nil, fmt.Errorf("versi/algoritma envelope snapshot tidak didukung")
	}
	if envelope.KeyID != c.keyID {
		return nil, fmt.Errorf("key ID snapshot tidak cocok: %s", envelope.KeyID)
	}

	nonce, err := base64.StdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, fmt.Errorf("nonce snapshot tidak valid: %w", err)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("ciphertext snapshot tidak valid: %w", err)
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, fmt.Errorf("membuat AES cipher gagal: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("membuat AES-GCM gagal: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("ukuran nonce snapshot tidak valid")
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("autentikasi snapshot gagal")
	}
	return plaintext, nil
}
