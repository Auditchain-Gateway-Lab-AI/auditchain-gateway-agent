#!/bin/sh
set -eu

# Read-only production/staging verification. This script deliberately does
# not delete or overwrite an object; destructive Object Lock tests belong in a
# disposable staging bucket and must be run manually.
endpoint="${MINIO_ENDPOINT:?MINIO_ENDPOINT is required}"
bucket="${MINIO_BUCKET:?MINIO_BUCKET is required}"
root_user="${MINIO_ROOT_USER:?MINIO_ROOT_USER is required}"
root_password="${MINIO_ROOT_PASSWORD:?MINIO_ROOT_PASSWORD is required}"

mc alias set verify "$endpoint" "$root_user" "$root_password" >/dev/null

echo "== versioning/object-lock bucket state =="
mc version info "verify/$bucket"
echo "== default retention =="
mc retention info "verify/$bucket"
echo "== object versions (inventory) =="
mc ls --recursive --versions "verify/$bucket"
echo "Compliance inspection completed (read-only)."

