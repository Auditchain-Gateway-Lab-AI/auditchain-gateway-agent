package snapshotstore

import (
	"bytes"
	"strings"
	"testing"
)

func TestCipherRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	cipher, err := NewCipher("key-test", key)
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}

	plaintext := []byte(`{"log_id":"L2","metadata":{"diagnosis":"sakit demam"}}`)
	ciphertext, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	if bytes.Contains(ciphertext, []byte("sakit demam")) {
		t.Fatal("ciphertext masih memuat plaintext metadata")
	}

	decrypted, err := cipher.Decrypt(ciphertext)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("plaintext hasil decrypt berbeda: %s", decrypted)
	}
}

func TestCipherAccepts64CharacterHexKey(t *testing.T) {
	key := strings.Repeat("ab", 32)
	cipher, err := NewCipherFromEncodedKey("key-hex", key)
	if err != nil {
		t.Fatalf("hex key ditolak: %v", err)
	}
	if cipher == nil {
		t.Fatal("cipher kosong")
	}
}

func TestCipherRejectsWrongKey(t *testing.T) {
	cipher, err := NewCipher("key-test", bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("NewCipher() error = %v", err)
	}
	ciphertext, err := cipher.Encrypt([]byte("audit snapshot"))
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}

	wrongCipher, err := NewCipher("key-test", bytes.Repeat([]byte{0x24}, 32))
	if err != nil {
		t.Fatalf("NewCipher(wrong key) error = %v", err)
	}
	if _, err := wrongCipher.Decrypt(ciphertext); err == nil {
		t.Fatal("Decrypt() dengan key salah seharusnya gagal")
	}
}

func TestNewCipherRejectsInvalidKeyLength(t *testing.T) {
	if _, err := NewCipher("key-test", []byte("terlalu-pendek")); err == nil {
		t.Fatal("NewCipher() menerima key yang bukan 32 byte")
	}
}
