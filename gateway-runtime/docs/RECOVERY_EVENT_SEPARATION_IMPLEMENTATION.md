# Recovery Event Separation — Backend Implementation

Dokumen ini mencatat perubahan backend untuk memisahkan bukti operasi recovery
dari `audit_logs`. Scope tetap hanya PostgreSQL AuditChain Gateway, MinIO,
Fabric, dan API Gateway; database operasional client tidak ditulis oleh alur
recovery.

## Model data

### `audit_logs`

`audit_logs` hanya berisi event CDC/operasional yang berasal dari client,
misalnya `INSERT`, `UPDATE`, dan `DELETE`. Event legacy dengan `action=RECOVERY`
tidak dihapus agar bukti historis tetap tersedia, tetapi tidak lagi ditampilkan
di daftar audit normal, statistik dashboard, inventory resource, atau Merkle
batch audit baru.

### `recovery_events`

Setiap eksekusi recovery membuat satu baris pada tabel `recovery_events`. Baris
ini menyimpan target, request/incident, before/after hash, hasil eksekusi,
referensi snapshot sumber yang dibekukan, event hash immutable, dan pipeline
snapshot/Merkle/Fabric miliknya sendiri.

Field sumber dan pelaksana sengaja dipisahkan:

- `source_system` tetap berasal dari snapshot event client, misalnya
  `SIMRS Morbis 1`; `target_source_system` disimpan sebagai alias
  kompatibilitas;
- `executor_system` menjelaskan komponen penulis, yaitu `AuditChain Gateway`;
- `executed_by` menyimpan user client yang menjalankan recovery.

## Alur client self-service

1. User client membaca incident dan kandidat pada tenant dari JWT.
2. `preflight` memeriksa exact MinIO version, ciphertext checksum, AES-GCM,
   snapshot hash, Merkle proof, dan anchor Fabric.
3. Client membuat request dengan idempotency key.
4. Client menjalankan request yang berstatus `PENDING_EXECUTION`; tidak ada
   approval platform-admin pada route baru.
5. Dalam transaksi PostgreSQL, target dipulihkan, tampered evidence disimpan
   terenkripsi, incident ditutup, dan `recovery_events` + outbox dibuat.
6. Snapshot worker menyimpan event ke namespace MinIO terpisah:
   `.../<client>/recovery-events/<date>/<event-id>/<event-hash>.snapshot`.
7. Recovery event diagregasi dan di-anchor ke Fabric secara terpisah dari
   Merkle tree audit client.

Semua operasi recovery memaksa `client_id` dari JWT. Parameter query tidak dapat
digunakan untuk berpindah tenant.

## API untuk frontend

Semua endpoint memakai `Authorization: Bearer <JWT>` dan prefix `/api`.

```text
GET  /api/dashboard/recovery/events
GET  /api/dashboard/recovery/events/:id
GET  /api/dashboard/recovery/events/:id/verify
```

Query `GET /events` mendukung `page`, `page_size` (maksimal 100),
`result_status`, `resource`, dan `include_legacy=false`. Event baru memiliki
`storage_kind=RECOVERY_EVENT` dan `legacy=false`. Baris lama memiliki
`storage_kind=LEGACY_AUDIT_LOG` dan `legacy=true`.

## Migrasi dan rollout

`RecoveryEvent` ditambahkan ke `AutoMigrate`, sehingga deployment membuat tabel
`recovery_events` dan indeks tenant/request/event hash. Tidak ada backfill dari
`audit_logs`; baris legacy dipertahankan sebagai read-only compatibility data.

Setelah deploy, cek startup migration, tabel `recovery_events`, dan
`snapshot_outboxes.event_type`; lakukan satu tamper-recovery staging sebagai
user client; pastikan target kembali valid, satu recovery event dan snapshotnya
muncul; lalu pastikan endpoint audit normal tidak mengembalikan
`action=RECOVERY`. Frontend memakai endpoint recovery events untuk Recovery
Center.

`recovery_events.result_status` menunjukkan hasil operasi recovery. Status
integrity event (`VALID`, `TAMPERED`, `PENDING`, `UNREACHABLE`) terpisah dari
status target audit dan status Agent client.
