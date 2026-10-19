package snapshotstore

import (
	"fmt"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestIsNotFoundUnwrapsMinIOError(t *testing.T) {
	err := fmt.Errorf("stat failed: %w", minio.ErrorResponse{Code: "NoSuchVersion", StatusCode: 404})
	if !IsNotFound(err) {
		t.Fatalf("expected wrapped MinIO not-found error to be detected")
	}
}

func TestIsNotFoundRejectsOtherErrors(t *testing.T) {
	err := fmt.Errorf("stat failed: %w", minio.ErrorResponse{Code: "AccessDenied", StatusCode: 403})
	if IsNotFound(err) {
		t.Fatalf("did not expect access denied to be classified as not found")
	}
}

func TestValidateObjectInfoRequiresExactImmutableReference(t *testing.T) {
	info := ObjectInfo{
		Key:         "production/client/log.snapshot",
		VersionID:   "version-1",
		ChecksumSHA: "checksum-1",
	}

	if err := ValidateObjectInfo(info, info.Key, info.VersionID, info.ChecksumSHA); err != nil {
		t.Fatalf("valid exact object reference rejected: %v", err)
	}
	if err := ValidateObjectInfo(info, info.Key, "version-2", info.ChecksumSHA); err == nil || err.Error() != "snapshot_version_mismatch" {
		t.Fatalf("wrong version error = %v, want snapshot_version_mismatch", err)
	}
	if err := ValidateObjectInfo(info, info.Key, info.VersionID, "checksum-2"); err == nil || err.Error() != "snapshot_checksum_mismatch" {
		t.Fatalf("wrong checksum error = %v, want snapshot_checksum_mismatch", err)
	}
}
