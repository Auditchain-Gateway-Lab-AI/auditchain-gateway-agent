# Implement Plan Penyelesaian Tamper Recovery AuditChain

Dokumen ini adalah addendum implementasi untuk menutup gap recovery AuditChain.
Scope hanya backend Gateway, PostgreSQL AuditChain, MinIO, Hyperledger Fabric,
dan deployment server. Database operasional milik client tidak pernah dibaca
atau diubah oleh recovery.

## Status implementasi

| Komponen | Status | Catatan |
|---|---|---|
| Snapshot terenkripsi + version ID | Implemented | Ditulis melalui transactional outbox dan snapshot worker. |
| Checksum exact object saat recovery | Implemented | SHA-256 ciphertext dibandingkan dengan referensi request. |
| Hash snapshot + Merkle/Fabric preflight | Implemented | Dipakai oleh preflight dan execute; execute selalu validasi ulang. |
| Frozen recovery references | Implemented | Request menyimpan checksum, plaintext hash, anchor ID, dan expected root. |
| Tamper scanner | Implemented | Batch scanner memeriksa log ANCHORED secara berkala dan meng-cache pembacaan anchor Fabric per anchor dalam satu siklus. |
| API candidates/preflight | Implemented | Kontrak siap digunakan dashboard. |
| PostgreSQL anchoring gate | Implemented in code | Recovery aktif fail-fast jika `SNAPSHOT_REQUIRED_FOR_ANCHOR` belum true; operator tetap harus mengaktifkannya di server. |
| Object Lock COMPLIANCE | Implemented for new scope | Object baru memakai COMPLIANCE 30 hari; object legacy tidak dimigrasikan karena berada di luar recovery scope. |
| Dashboard Recovery Center | Out of scope | Dikerjakan pada repository frontend terpisah. |
| Legacy recovery boundary | Implemented in code | `RECOVERY_CUTOFF_AT` membatasi recovery pada log baru; history lama tetap dapat diverifikasi tetapi tidak dipulihkan dari MinIO. |
| Status Agent vs Gateway vs recovery | Implemented | `chain_status`/`integrity_status` hanya merepresentasikan Gateway + Fabric; `agent_status` berdiri sendiri; `recovery_status` menandai `not_recovered`, `pending`, `recovered`, atau `failed`. Agent offline tidak lagi membuat log Gateway yang valid tampil sebagai `unreachable`. |

## Urutan validasi recovery

Sebelum row audit_logs diubah, service wajib:

1. Membaca object menggunakan snapshot_object_key dan snapshot_version_id.
2. Memastikan Version ID yang dikembalikan sama persis.
3. Membandingkan SHA-256 ciphertext dengan snapshot_checksum.
4. Mendekripsi envelope AES-256-GCM.
5. Memvalidasi schema, log_id, dan client_id.
6. Menghitung ulang SHA3-256 canonical audit log.
7. Membandingkan hasilnya dengan snapshot_plaintext_hash.
8. Mengambil Merkle Proof dari PostgreSQL.
9. Mengambil anchor menggunakan anchor_id dari request.
10. Membandingkan Merkle Root hasil rekonstruksi dengan Fabric.
11. Memulihkan row dalam transaksi PostgreSQL.
12. Membaca ulang row dan memastikan hash identik.

Jika satu langkah gagal, row tidak boleh diubah. Nilai tampered hanya disimpan
sebagai evidence terenkripsi setelah seluruh validasi snapshot berhasil.

Recovery hanya boleh memakai snapshot dengan log_id yang sama dengan incident.
Histori resource dapat ditampilkan sebagai informasi, tetapi tidak dapat dipakai
untuk menimpa event audit lain.

> Kolom referensi beku pada `recovery_requests` sengaja ditambahkan nullable pada
> level PostgreSQL agar `AutoMigrate` tidak gagal pada instalasi yang sudah
> memiliki request legacy. Request baru tetap fail-closed dan wajib mengisi
> seluruh referensi sebelum dapat dibuat.

## Scanner tamper

Feature flag dan default konfigurasi:

~~~text
TAMPER_SCANNER_ENABLED=false
TAMPER_SCAN_INTERVAL_SECONDS=300
TAMPER_SCAN_BATCH_SIZE=100
TAMPER_SCAN_CONCURRENCY=2
RECOVERY_CUTOFF_AT=2026-09-18T14:34:33Z
~~~

`RECOVERY_CUTOFF_AT` wajib diisi ketika `RECOVERY_ENABLED=true` atau
`SNAPSHOT_REQUIRED_FOR_ANCHOR=true`. Audit log dengan `db_timestamp` sebelum
cutoff (atau tanpa `db_timestamp`) dikategorikan sebagai legacy. Log tersebut
tetap berada di PostgreSQL dan dapat diverifikasi terhadap Fabric, tetapi tidak
ditawarkan sebagai kandidat recovery. Scanner terjadwal dan aggregator baru
juga tidak memproses ulang baris legacy.

Setelah sebuah log berhasil dipulihkan, recovery berikutnya untuk log yang sama
tidak dianggap sebagai recovery legacy baru. `recovery_events` yang sukses dan
memiliki referensi snapshot sumber, anchor, serta Merkle root membentuk trusted
lineage. Incident tamper berikutnya boleh memakai lineage tersebut, dengan
syarat seluruh validasi snapshot dan anchor tetap dijalankan ulang. Ini menjaga
agar satu log dapat dipulihkan berulang kali tanpa menghapus batasan untuk log
legacy yang belum pernah memiliki bukti snapshot terpercaya.

Scanner mengambil log berstatus ANCHORED yang paling lama diperiksa. Status
operasional disimpan pada audit_logs.integrity_status:

~~~text
NOT_CHECKED | VALID | TAMPERED | PENDING | UNREACHABLE
~~~

Scanner hanya memeriksa database Gateway dan Fabric. Scanner tidak memanggil
Agent atau database client. Saat hash/Merkle mismatch, scanner membuat satu
tamper_incident aktif secara idempoten. Saat Fabric tidak tersedia, status
menjadi UNREACHABLE dan tidak dibuat incident palsu.

Scanner tidak menjalankan recovery otomatis. Recovery selalu memerlukan request
yang dibuat oleh user client dan eksekusi eksplisit oleh user client dalam
tenant yang sama. Tidak ada approval platform-admin dalam workflow ini.

## Kontrak API untuk dashboard

Semua endpoint berada di bawah /api/dashboard/recovery dan memerlukan JWT.
Semua operasi recovery dibatasi pada `client_id` yang terdapat di token user;
query `client_id` tidak dapat digunakan untuk berpindah tenant. Platform-admin
tidak menjadi bagian dari workflow recovery.

### Kandidat recovery

~~~text
GET /api/dashboard/recovery/incidents/{incident_id}/candidates
~~~

Response sukses:

~~~json
{
  "data": [
    {
      "log_id": "1789651685834089911",
      "snapshot_object_key": "production/.../snapshot.snapshot",
      "snapshot_version_id": "version-id",
      "snapshot_checksum": "sha256-ciphertext",
      "snapshot_plaintext_hash": "sha3-audit-hash",
      "snapshot_verified_at": "2026-09-17T13:29:49Z",
      "eligible": true,
      "reason": ""
    }
  ]
}
~~~

### Preflight MinIO/Fabric

~~~text
POST /api/dashboard/recovery/incidents/{incident_id}/preflight
~~~

Preflight tidak mengubah PostgreSQL. Response valid:

~~~json
{
  "status": "VALID",
  "recoverable": true,
  "log_id": "1789651685834089911",
  "current_hash": "hash-yang-dihitung-dari-postgresql",
  "current_integrity": "TAMPERED",
  "snapshot_hash": "sha3-audit-hash",
  "merkle_root": "merkle-root",
  "anchor_id": "anchor-id",
  "object_version_id": "version-id",
  "snapshot_preview": {
    "actor": "user-indah",
    "action": "UPDATE",
    "resource": "DATA_RM:123",
    "timestamp": "2026-09-17T13:00:00Z",
    "metadata": {
      "diagnosis": "sakit demam"
    }
  }
}
~~~

`current_hash` pada preflight adalah hash yang dihitung ulang dari row
PostgreSQL saat itu. Recovery hanya `recoverable=true` jika row saat ini
berbeda dari snapshot tepercaya. Execute tetap melakukan seluruh validasi
ulang. Hasil preflight tidak menjadi izin permanen apabila object, database,
atau anchor berubah setelahnya.

Endpoint existing tetap dipakai:

~~~text
GET  /incidents
GET  /incidents/{id}
GET  /resources/{resource}/versions
GET  /requests
GET  /requests/{id}
POST /requests
POST /requests/{id}/execute
~~~

## Feature flag dan startup gate

Konfigurasi produksi setelah backlog snapshot sehat:

~~~text
SNAPSHOT_WRITER_ENABLED=true
SNAPSHOT_REQUIRED_FOR_ANCHOR=true
TAMPER_SCANNER_ENABLED=true
RECOVERY_ENABLED=true
MINIO_RETENTION_MODE=COMPLIANCE
MINIO_RETENTION_DAYS=30
RECOVERY_CUTOFF_AT=2026-09-18T14:34:33Z
~~~

Gateway gagal startup jika:

- anchoring gate aktif tanpa snapshot writer atau konfigurasi MinIO;
- anchoring gate aktif tanpa Fabric;
- recovery aktif tanpa MinIO, encryption key, snapshot builder, atau Fabric;
- scanner aktif tanpa Fabric.
- recovery atau anchoring gate aktif tanpa `RECOVERY_CUTOFF_AT`.
- recovery aktif tanpa `SNAPSHOT_REQUIRED_FOR_ANCHOR=true`.

Pada server, tambahkan flag tersebut ke `.env` yang sudah ada; jangan menimpa
file `.env` produksi dengan `.env.example` karena file itu berisi secret dan
endpoint lokal. Setelah baseline serta staging lulus, validasi konfigurasi dan
restart bertahap dengan `docker compose config --quiet` lalu
`docker compose up -d --build api-gateway`. Pastikan log startup menampilkan
tamper scanner aktif dan tidak ada error snapshot worker sebelum mengaktifkan
gate anchoring.

## Runbook MinIO COMPLIANCE

Jalankan pada maintenance window server:

1. Backup PostgreSQL dan inventarisasi semua object/version ID.
2. Pastikan bucket versioning dan Object Lock aktif.
3. Verifikasi waktu server/NTP.
4. Uji bucket staging dengan COMPLIANCE.
5. Uji update/delete menggunakan writer dan root; operasi harus ditolak selama retention.
6. Set default bucket production ke COMPLIANCE 30 hari untuk object baru.
7. Object legacy tidak dimigrasikan dan tidak boleh dipilih sebagai kandidat recovery.
8. Verifikasi mode COMPLIANCE dan retain-until pada object baru dengan mc stat atau mc retention info.
9. Jangan menghapus atau mengganti bucket sebelum seluruh bukti inventaris dan validasi tersimpan.

Pemeriksaan read-only dapat dijalankan menggunakan
`scripts/minio/verify-compliance.sh`. Ringkasan operasional untuk scope baru
tersedia di `scripts/ops/check-recovery-health.sh`; script tersebut sengaja
tidak menghitung baris legacy sebelum `RECOVERY_CUTOFF_AT`.

COMPLIANCE bersifat irreversible sampai masa retention berakhir. Gunakan
staging/ephemeral bucket untuk smoke test agar object test tidak bercampur
dengan bucket recovery produksi.

## Timeline implementasi backend/server

Estimasi satu developer, satu hari kerja intensif, dengan rollout bertahap:

| Waktu | Aktivitas | Output |
|---|---|---|
| H+0–0:45 | Backup, baseline flags, inventaris MinIO/Fabric | Baseline tervalidasi |
| H+0:45–3:00 | Frozen references, checksum, shared recovery validation | Recovery fail-closed |
| H+3:00–4:30 | Candidates/preflight API | Kontrak dashboard siap |
| H+4:30–6:00 | Tamper scanner dan startup validation | Scanner siap dijalankan |
| H+6:00–7:30 | Unit/integration test dan build image | Backend lulus verifikasi |
| H+7:30–9:15 | Deploy flag nonaktif, smoke-test Compliance, migrasi retention | Storage terlindungi |
| H+9:15–10:00 | Rekonsiliasi snapshot dan anchoring gate | Tidak ada anchor tanpa snapshot |
| H+10:00–11:00 | E2E tamper → preflight → client request → recovery | Recovery server terbukti |
| H+11:00–12:00 | Monitoring, API handoff, dan runbook | Handoff frontend/server |

## Acceptance test

1. Buat audit event dan pastikan snapshot VERIFIED.
2. Pastikan event menjadi ANCHORED.
3. Ubah metadata langsung di PostgreSQL AuditChain.
4. Tunggu scanner menandai TAMPERED dan membuat incident.
5. Jalankan preflight dan pastikan snapshot MinIO cocok dengan Fabric.
6. Buat request sebagai user client, lalu execute tanpa approval platform-admin.
7. Pastikan metadata kembali ke snapshot asli.
8. Pastikan hash hasil recovery cocok dengan anchor Fabric.
9. Pastikan evidence tampered tersimpan terenkripsi.
10. Pastikan event RECOVERY baru dibuat dan masuk outbox snapshot.
11. Pastikan database client tidak pernah disentuh.
12. Pastikan log sebelum `RECOVERY_CUTOFF_AT` tetap dapat diverifikasi tetapi
    kandidat recovery mengembalikan `eligible=false` dengan alasan
    `legacy_recovery_out_of_scope`.

Event `RECOVERY` merupakan audit event baru, tetapi kolom `metadata`-nya harus
identik dengan metadata snapshot yang dipulihkan dan actor sumbernya harus
mengikuti actor pada snapshot. Detail workflow recovery
(request, incident, target log, dan hash sebelum/sesudah) ditelusuri melalui
`recovery_requests`, `tamper_incidents`, dan `authorization_context`, bukan
dengan mengganti metadata canonical menjadi envelope workflow.

Failure test wajib meliputi checksum berbeda, Version ID salah, ciphertext rusak,
client/log ID berbeda, Merkle Proof salah, Fabric unavailable, request lintas
client, idempotency duplicate, dan percobaan delete object COMPLIANCE.

## Hard stop rollout

Jangan mengaktifkan RECOVERY_ENABLED, TAMPER_SCANNER_ENABLED, atau
SNAPSHOT_REQUIRED_FOR_ANCHOR jika backup gagal, Fabric tidak dapat dibaca,
exact MinIO version tidak dapat dibaca, backlog snapshot belum sehat, E2E gagal,
atau object COMPLIANCE masih dapat dihapus.

Dashboard/frontend berada di repository terpisah. Dokumen ini menjadi kontrak
backend untuk implementasi Recovery Center oleh developer frontend.
