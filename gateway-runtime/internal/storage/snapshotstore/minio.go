package snapshotstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"go-blockchain-api/internal/config"
)

const defaultMaxSnapshotBytes = int64(16 * 1024 * 1024)

type MinIOStore struct {
	client           *minio.Client
	bucket           string
	maxSnapshotBytes int64
}

func NewMinIOStore(cfg config.MinIOConfig) (*MinIOStore, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("inisialisasi client MinIO gagal: %w", err)
	}

	return &MinIOStore{
		client:           client,
		bucket:           cfg.Bucket,
		maxSnapshotBytes: defaultMaxSnapshotBytes,
	}, nil
}

func (s *MinIOStore) Put(ctx context.Context, key string, payload []byte, metadata map[string]string) (ObjectInfo, error) {
	if err := validatePayloadSize(int64(len(payload)), s.maxSnapshotBytes); err != nil {
		return ObjectInfo{}, err
	}

	options := minio.PutObjectOptions{
		ContentType:  "application/octet-stream",
		UserMetadata: metadata,
	}
	info, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(payload), int64(len(payload)), options)
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("upload snapshot %q gagal: %w", key, err)
	}

	return ObjectInfo{
		Key:         key,
		VersionID:   info.VersionID,
		ETag:        info.ETag,
		Size:        info.Size,
		ContentType: options.ContentType,
		ChecksumSHA: checksumSHA256(payload),
	}, nil
}

func (s *MinIOStore) GetVersion(ctx context.Context, key, versionID string) ([]byte, ObjectInfo, error) {
	if versionID == "" {
		return nil, ObjectInfo{}, fmt.Errorf("version ID wajib diisi untuk membaca snapshot %q", key)
	}

	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{VersionID: versionID})
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("membuka snapshot %q versi %q gagal: %w", key, versionID, err)
	}
	defer object.Close()

	stat, err := object.Stat()
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("snapshot %q versi %q tidak dapat di-stat: %w", key, versionID, err)
	}
	if err := validatePayloadSize(stat.Size, s.maxSnapshotBytes); err != nil {
		return nil, ObjectInfo{}, err
	}

	payload, err := io.ReadAll(io.LimitReader(object, s.maxSnapshotBytes+1))
	if err != nil {
		return nil, ObjectInfo{}, fmt.Errorf("membaca snapshot %q versi %q gagal: %w", key, versionID, err)
	}
	if err := validatePayloadSize(int64(len(payload)), s.maxSnapshotBytes); err != nil {
		return nil, ObjectInfo{}, err
	}

	info := objectInfoFromStat(key, stat)
	info.ChecksumSHA = checksumSHA256(payload)
	return payload, info, nil
}

func (s *MinIOStore) StatVersion(ctx context.Context, key, versionID string) (ObjectInfo, error) {
	if versionID == "" {
		return ObjectInfo{}, fmt.Errorf("version ID wajib diisi untuk stat snapshot %q", key)
	}

	stat, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{VersionID: versionID})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat snapshot %q versi %q gagal: %w", key, versionID, err)
	}
	return objectInfoFromStat(key, stat), nil
}

func (s *MinIOStore) StatLatest(ctx context.Context, key string) (ObjectInfo, error) {
	stat, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		return ObjectInfo{}, fmt.Errorf("stat snapshot terbaru %q gagal: %w", key, err)
	}
	return objectInfoFromStat(key, stat), nil
}

func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var response minio.ErrorResponse
	if !errors.As(err, &response) {
		response = minio.ToErrorResponse(err)
	}
	switch response.Code {
	case "NoSuchKey", "NoSuchVersion", "NotFound", "XMinioInvalidObjectName":
		return true
	default:
		return response.StatusCode == 404
	}
}

func objectInfoFromStat(key string, stat minio.ObjectInfo) ObjectInfo {
	return ObjectInfo{
		Key:          key,
		VersionID:    stat.VersionID,
		ETag:         stat.ETag,
		Size:         stat.Size,
		ContentType:  stat.ContentType,
		UserMetadata: stat.UserMetadata,
	}
}

func validatePayloadSize(size, max int64) error {
	if size < 0 {
		return fmt.Errorf("ukuran snapshot tidak valid: %d", size)
	}
	if max > 0 && size > max {
		return fmt.Errorf("ukuran snapshot %d byte melebihi batas %d byte", size, max)
	}
	return nil
}

func checksumSHA256(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// ValidateObjectInfo checks the immutable reference captured in a recovery
// request. Recovery callers must use this before decrypting or applying a
// snapshot so a different object version cannot be substituted silently.
func ValidateObjectInfo(info ObjectInfo, expectedKey, expectedVersion, expectedChecksum string) error {
	if strings.TrimSpace(expectedKey) == "" || strings.TrimSpace(expectedVersion) == "" {
		return errors.New("snapshot_reference_missing")
	}
	if info.Key != expectedKey {
		return errors.New("snapshot_key_mismatch")
	}
	if info.VersionID != expectedVersion {
		return errors.New("snapshot_version_mismatch")
	}
	if strings.TrimSpace(expectedChecksum) == "" {
		return errors.New("snapshot_checksum_missing")
	}
	if info.ChecksumSHA != expectedChecksum {
		return errors.New("snapshot_checksum_mismatch")
	}
	return nil
}
