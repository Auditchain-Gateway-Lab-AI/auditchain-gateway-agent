# Recovery API Contract

Semua endpoint memakai prefix `/api` dan JWT `Authorization: Bearer <token>`.
Untuk recovery direct, `client_id` selalu diambil dari JWT; query parameter
`client_id` tidak dapat dipakai untuk berpindah tenant. User client dapat
menjalankan recovery milik tenant-nya sendiri tanpa approval platform-admin.

## Mode runtime

```dotenv
RECOVERY_ENABLED=true
RECOVERY_MODE=agent_direct
RECOVERY_CDC_TIMEOUT_SECONDS=120
```

`agent_direct` adalah jalur baru: Fabric menjadi bukti anchor, PostgreSQL
AuditChain menjadi referensi event/metadata, dan Agent menjadi satu-satunya
komponen yang menulis row database operasional client. `snapshot_legacy` tetap
tersedia sementara untuk kompatibilitas/rollback, tetapi memakai alur MinIO
lama dan tidak boleh dicampur dengan request direct.

## Endpoint

```text
GET  /api/dashboard/recovery/incidents?status=OPEN
GET  /api/dashboard/recovery/incidents/:incident_id
GET  /api/dashboard/recovery/incidents/:incident_id/candidates
POST /api/dashboard/recovery/incidents/:incident_id/preflight
GET  /api/dashboard/recovery/resources/:resource/versions
GET  /api/dashboard/recovery/requests?status=PENDING_EXECUTION
GET  /api/dashboard/recovery/requests/:request_id
POST /api/dashboard/recovery/requests
POST /api/dashboard/recovery/requests/:request_id/execute
GET  /api/dashboard/recovery/events
GET  /api/dashboard/recovery/events/:event_id
GET  /api/dashboard/recovery/events/:event_id/verify
```

Endpoint `approve` dan `reject` tidak didaftarkan pada router direct. Method
lama masih dipertahankan hanya untuk kompatibilitas kode legacy, bukan sebagai
workflow baru.

## Direct preflight

`POST .../preflight` bersifat read-only dan melakukan pemeriksaan ulang:

1. incident berada pada tenant JWT dan berscope `CLIENT_SOURCE`;
2. target adalah event client terbaru untuk resource, bukan event `RECOVERY`;
3. hash payload PostgreSQL, leaf, ordered Merkle proof, root, dan anchor Fabric
   cocok;
4. state terkini dibaca dari Agent melalui `GET /verify/:table/:id`;
5. state live dibandingkan dengan state referensi setelah canonicalization.

Jika Agent atau Fabric tidak dapat dihubungi, preflight gagal tertutup dan
tidak ada write. `agent_status=unreachable` tidak mengubah `integrity_status`
Gateway menjadi `TAMPERED`.

Response direct memuat minimal:

```json
{
  "status": "VALID",
  "recoverable": true,
  "operation": "UPSERT",
  "log_id": "...",
  "reference_log_hash": "...",
  "reference_merkle_root": "...",
  "reference_anchor_id": "...",
  "fabric_root": "...",
  "client_state_hash": "...",
  "desired_state_hash": "...",
  "source_status": "MISMATCH",
  "agent_status": "mismatch",
  "snapshot_preview": {}
}
```

Nilai metadata/state yang sensitif harus mengikuti redaction policy. Hash dan
proof tetap boleh ditampilkan sebagai evidence teknis.

## Membuat dan mengeksekusi request

```text
POST /api/dashboard/recovery/requests
POST /api/dashboard/recovery/requests/:request_id/execute
```

Body create:

```json
{
  "incident_id": "incident-uuid",
  "selected_log_id": "exact-client-log-id",
  "reason": "alasan recovery",
  "idempotency_key": "client-generated-unique-key"
}
```

Request direct dibuat sebagai `PENDING_EXECUTION`. Gateway membekukan:
referensi Fabric, desired state, `client_before_hash`, operasi (`UPSERT` atau
`DELETE`), command id Agent, dan batas waktu CDC. Request aktif kedua untuk
tenant/resource yang sama ditolak.

Execute mengulang validasi referensi dan precondition, kemudian mengirim
command berikut ke Agent:

```text
POST /recover/:table/:record_id
Authorization: Bearer <AGENT_RECOVERY_TOKEN>
```

Payload tidak boleh berisi SQL, predicate, nama kolom arbitrer, atau instruksi
untuk membuat tabel. Mapping tabel/primary-key dan allowlist kolom tetap lokal
di Agent. Gateway mewajibkan response readback cocok dengan
`desired_state_hash`.

## State dan finalisasi

Urutan status direct:

```text
PENDING_EXECUTION
  -> EXECUTING
  -> APPLIED_AWAITING_CDC
  -> SUCCEEDED
```

CDC yang tidak datang menjadi `APPLIED_CDC_TIMEOUT`; kegagalan precondition,
Agent, atau readback menjadi `FAILED_*`. Request tidak boleh `SUCCEEDED` hanya
karena HTTP Agent mengembalikan 2xx.

Debezium harus menangkap event client hasil write. Event recovery internal
disimpan pada tabel `recovery_events`, bukan sebagai row `audit_logs`. Event
tersebut kemudian melewati hashing, Merkle, dan verifikasi Fabric. Hanya setelah
event recovery terverifikasi, request menjadi `SUCCEEDED` dan incident menjadi
`RESOLVED`.

## Pemisahan field sumber dan pelaksana

- `source_system`: sistem sumber event yang dipulihkan, misalnya `SIMRS Morbis 1`;
- `target_source_system`: alias kompatibilitas untuk source target;
- `executor_system`: komponen yang mengirim command, yaitu `AuditChain Gateway`;
- `executed_by`: user client pada JWT.

Dengan pemisahan ini, actor/source data client tidak berubah menjadi actor
Gateway hanya karena Gateway menjalankan command recovery.

## Legacy snapshot mode

Mode `snapshot_legacy` tetap memakai validasi object version, checksum,
AES-GCM, snapshot hash, Merkle proof, dan anchor Fabric. Mode tersebut hanya
untuk compatibility/rollback; tidak boleh dianggap sebagai implementasi direct
client-DB. Data lama yang tidak memiliki snapshot valid tetap tidak eligible.
