package snapshotstore

import "context"

// ObjectInfo is the minimum storage metadata needed by the recovery workflow.
// VersionID must be persisted and used for recovery reads; reading "latest"
// is intentionally not part of this interface.
type ObjectInfo struct {
	Key          string
	VersionID    string
	ETag         string
	Size         int64
	ContentType  string
	ChecksumSHA  string
	UserMetadata map[string]string
}

type SnapshotStore interface {
	Put(ctx context.Context, key string, payload []byte, metadata map[string]string) (ObjectInfo, error)
	GetVersion(ctx context.Context, key, versionID string) ([]byte, ObjectInfo, error)
	StatVersion(ctx context.Context, key, versionID string) (ObjectInfo, error)
}

// LatestObjectStore is used only by the writer to recover from a crash after
// MinIO accepted a PUT but before PostgreSQL recorded the resulting version.
// Recovery reads still require an explicit version ID through SnapshotStore.
type LatestObjectStore interface {
	SnapshotStore
	StatLatest(ctx context.Context, key string) (ObjectInfo, error)
}
