#!/bin/sh
set -eu

# Read-only operational snapshot for the new recovery scope. Legacy rows
# before RECOVERY_CUTOFF_AT are intentionally excluded from the health count.
db_container="${AUDITCHAIN_DB_CONTAINER:-auditchain-postgres}"
db_name="${AUDITCHAIN_DB_NAME:-test_blockchain}"
db_user="${AUDITCHAIN_DB_USER:-postgres}"
cutoff="${RECOVERY_CUTOFF_AT:?RECOVERY_CUTOFF_AT is required}"

docker exec "$db_container" psql -U "$db_user" -d "$db_name" \
  -v recovery_cutoff="$cutoff" -P pager=off -c "
WITH scoped AS (
  SELECT *
  FROM audit_logs
  WHERE db_timestamp IS NOT NULL
    AND db_timestamp >= :'recovery_cutoff'::timestamptz
), outbox AS (
  SELECT status, COUNT(*) AS total
  FROM snapshot_outboxes
  WHERE created_at >= :'recovery_cutoff'::timestamptz
  GROUP BY status
)
SELECT
  (SELECT COUNT(*) FROM scoped) AS scoped_logs,
  (SELECT COUNT(*) FROM scoped WHERE status = 'ANCHORED' AND snapshot_status = 'VERIFIED') AS anchored_verified,
  (SELECT COUNT(*) FROM scoped WHERE status = 'ANCHORED' AND snapshot_status <> 'VERIFIED') AS anchored_without_verified_snapshot,
  (SELECT COUNT(*) FROM scoped WHERE integrity_status = 'TAMPERED') AS tampered,
  (SELECT COUNT(*) FROM scoped WHERE integrity_status = 'UNREACHABLE') AS unreachable,
  (SELECT COALESCE(SUM(total) FILTER (WHERE status = 'DEAD_LETTER'), 0) FROM outbox) AS outbox_dead_letter;

SELECT status, total
FROM outbox
ORDER BY status;
"

