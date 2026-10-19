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
AuditChain menjadi referensi event/metadata, dan Agent membaca riwayat sumber
serta menjadi satu-satunya komponen yang menulis row operasional client.
Gateway audit-log metadata dipulihkan dari event sumber Agent setelah hash,
Merkle proof, dan Fabric anchor lolos verifikasi; alur ini tidak menggunakan
MinIO. Mode `snapshot_legacy` hanya tersisa untuk kompatibilitas deployment lama.

Tidak ada fallback ke metadata dari browser atau payload incident. Pemulihan
Gateway hanya tersedia jika `source_record_id` pada audit log menunjuk ke event
`audit_trail` yang masih dapat dibaca Agent.

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

## Gateway audit-log metadata recovery tanpa MinIO

`GET /api/dashboard/verify/:log_id` tetap merupakan endpoint deteksi integritas.
Respons `409 Conflict` dengan `failed_local` berarti metadata lokal berbeda dari
hash AuditChain; respons itu bukan kegagalan endpoint dan bukan operasi restore.
Setelah incident tercatat, kandidat/preflight `GATEWAY_INTEGRITY` membaca event
asli melalui Agent (`GET /verify-audit/:source_record_id`), bukan
`GET /verify/:table/:id` yang hanya membaca row operasional saat ini.

Pemulihan hanya boleh berjalan jika metadata `data_lama`/`data_baru` dari Agent
mereproduksi hash leaf yang sudah tersimpan, ordered Merkle proof mereproduksi
root yang sama, dan Fabric mengembalikan root tersebut. Gateway kemudian
memperbarui hanya `audit_logs.metadata` beserta status integritas, memverifikasi
hash readback, dan mencatat recovery event baru untuk pipeline Merkle/Fabric.
Jika source event tidak ditemukan, referensi tidak cocok, atau salah satu bukti
gagal diverifikasi, request ditolak tanpa mengubah audit log.

Agent membutuhkan akses baca ke tabel sejarah sumber. Default nama tabel adalah
`AUDIT_TRAIL`; atur `AGENT_AUDIT_TRAIL_TABLE` dan, jika perlu,
`AGENT_AUDIT_TRAIL_SCHEMA` pada Agent. `source_record_id` wajib terisi pada
`audit_logs`; log lama tanpa referensi tersebut tidak dapat dipulihkan lewat
jalur ini.

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

## Snapshot compatibility mode

Mode `snapshot_legacy` masih memakai validasi object version, checksum, AES-GCM,
snapshot hash, Merkle proof, dan anchor Fabric. Mode ini hanya untuk rollback
deployment lama yang masih memiliki snapshot store; recovery aktif pada
`agent_direct` tidak bergantung pada snapshot atau MinIO.
