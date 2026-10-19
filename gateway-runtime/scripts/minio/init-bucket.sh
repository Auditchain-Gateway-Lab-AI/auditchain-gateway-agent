#!/bin/sh
set -eu

endpoint="${MINIO_ENDPOINT:?MINIO_ENDPOINT is required}"
bucket="${MINIO_BUCKET:?MINIO_BUCKET is required}"
root_user="${MINIO_ROOT_USER:?MINIO_ROOT_USER is required}"
root_password="${MINIO_ROOT_PASSWORD:?MINIO_ROOT_PASSWORD is required}"
writer_user="${MINIO_WRITER_USER:?MINIO_WRITER_USER is required}"
writer_password="${MINIO_WRITER_PASSWORD:?MINIO_WRITER_PASSWORD is required}"
reader_user="${MINIO_READER_USER:?MINIO_READER_USER is required}"
reader_password="${MINIO_READER_PASSWORD:?MINIO_READER_PASSWORD is required}"
retention_mode="${MINIO_RETENTION_MODE:?MINIO_RETENTION_MODE is required}"
retention_days="${MINIO_RETENTION_DAYS:?MINIO_RETENTION_DAYS is required}"
app_env="${APP_ENV:-local}"

case "$retention_mode" in
  COMPLIANCE|GOVERNANCE) ;;
  *) echo "MINIO_RETENTION_MODE must be COMPLIANCE or GOVERNANCE" >&2; exit 1 ;;
esac
case "$retention_days" in
  ''|*[!0-9]*) echo "MINIO_RETENTION_DAYS must be a positive integer" >&2; exit 1 ;;
esac
if [ "$retention_days" -le 0 ]; then
  echo "MINIO_RETENTION_DAYS must be greater than zero" >&2
  exit 1
fi
if [ "$app_env" = "production" ] && [ "$retention_mode" != "COMPLIANCE" ]; then
  echo "production requires MINIO_RETENTION_MODE=COMPLIANCE" >&2
  exit 1
fi

mc alias set local "$endpoint" "$root_user" "$root_password" >/dev/null

# --with-lock creates the bucket with versioning/object locking enabled. If a
# pre-existing bucket was created without locking, fail instead of silently
# accepting a bucket that cannot provide the recovery guarantee.
if ! mc ls "local/$bucket" >/dev/null 2>&1; then
  mc mb --with-lock "local/$bucket"
fi

mc version enable "local/$bucket" >/dev/null 2>&1 || true
mc retention set --default "$retention_mode" "${retention_days}d" "local/$bucket"

# The mc image is intentionally minimal and does not include sed/cat. Render
# the two least-privilege policies using the POSIX shell's printf builtin so a
# staging/production bucket override still receives the same policy.
writer_policy_file="/tmp/audit-snapshot-writer.json"
reader_policy_file="/tmp/audit-snapshot-reader.json"
printf '%s\n' \
  '{' \
  '  "Version": "2012-10-17",' \
  '  "Statement": [' \
  '    {' \
  '      "Effect": "Allow",' \
  '      "Action": ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"],' \
  '      "Resource": ["arn:aws:s3:::'"$bucket"'"]' \
  '    },' \
  '    {' \
  '      "Effect": "Allow",' \
  '      "Action": ["s3:AbortMultipartUpload", "s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectVersion", "s3:GetObjectVersionAttributes", "s3:ListMultipartUploadParts", "s3:PutObject"],' \
  '      "Resource": ["arn:aws:s3:::'"$bucket"'/*"]' \
  '    }' \
  '  ]' \
  '}' > "$writer_policy_file"

printf '%s\n' \
  '{' \
  '  "Version": "2012-10-17",' \
  '  "Statement": [' \
  '    {' \
  '      "Effect": "Allow",' \
  '      "Action": ["s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketVersions"],' \
  '      "Resource": ["arn:aws:s3:::'"$bucket"'"]' \
  '    },' \
  '    {' \
  '      "Effect": "Allow",' \
  '      "Action": ["s3:GetObject", "s3:GetObjectAttributes", "s3:GetObjectVersion", "s3:GetObjectVersionAttributes"],' \
  '      "Resource": ["arn:aws:s3:::'"$bucket"'/*"]' \
  '    }' \
  '  ]' \
  '}' > "$reader_policy_file"

create_policy_if_missing() {
  policy_name="$1"
  policy_file="$2"
  if ! mc admin policy info local "$policy_name" >/dev/null 2>&1; then
    mc admin policy create local "$policy_name" "$policy_file"
  fi
}

create_user_if_missing() {
  username="$1"
  password="$2"
  if ! mc admin user info local "$username" >/dev/null 2>&1; then
    mc admin user add local "$username" "$password"
  fi
}

create_policy_if_missing audit-snapshot-writer "$writer_policy_file"
create_policy_if_missing audit-snapshot-reader "$reader_policy_file"
create_user_if_missing "$writer_user" "$writer_password"
create_user_if_missing "$reader_user" "$reader_password"

mc admin policy attach local audit-snapshot-writer --user "$writer_user"
mc admin policy attach local audit-snapshot-reader --user "$reader_user"

echo "MinIO bucket '$bucket' is ready with Object Lock and ${retention_mode} retention for ${retention_days} days."
