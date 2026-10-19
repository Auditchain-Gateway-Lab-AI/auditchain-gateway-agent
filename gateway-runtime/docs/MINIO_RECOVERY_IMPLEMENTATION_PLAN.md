# Implementation Plan dan Timeline MinIO Recovery

> Catatan workflow terbaru: bagian dokumen yang masih menyebut approval
> platform-admin adalah rancangan historis. Implementasi aktif memakai
> self-service recovery oleh user client yang terikat pada `client_id` token;
> lihat `MINIO_RECOVERY_GAP_COMPLETION_PLAN.md` dan `RECOVERY_API_CONTRACT.md`.

## 1. Informasi Dokumen

| Item | Nilai |
|---|---|
| Proyek | AuditChain Gateway Backend |
| Dokumen | Rencana implementasi snapshot immutable dan recovery menggunakan MinIO |
| Tanggal penyusunan | 17 September 2026 |
| Status | Implementasi backend tahap lokal sedang berjalan |
| Branch yang diperiksa | `nama-fitur-baru` |
| Commit baseline | `4ad6fe0` (`add immudb ui`) |
| Target awal | Lokal menggunakan Docker Compose |
| Target lanjutan | Staging lalu production satu server |

Dokumen ini menjadi urutan kerja resmi agar implementasi tidak dilakukan secara acak, tidak melompati dependensi, dan dapat diverifikasi pada setiap tahap.

### Status eksekusi per 17 September 2026

| Area | Status | Bukti atau batasan |
|---|---|---|
| Compose MinIO, initializer, Object Lock, retention, policy | Selesai secara kode | `docker-compose config` dan `sh -n` lolos; runtime belum dapat diuji karena Docker Engine lokal belum aktif |
| Snapshot AES-GCM, format schema, MinIO adapter | Selesai secara kode | Unit test `internal/storage/snapshotstore` lolos |
| PostgreSQL outbox, retry, lease, idempotensi, worker | Selesai secara kode | Unit test `internal/engine/snapshotworker` dan `go test ./...` lolos |
| Gating Merkle/Fabric berdasarkan snapshot | Selesai secara kode | Aktif bila `SNAPSHOT_REQUIRED_FOR_ANCHOR=true` |
| Tamper incident dan recovery API | Selesai secara kode untuk MVP | Endpoint gated `RECOVERY_ENABLED=true`; recovery hanya target `log_id` yang sama |
| Recovery ke database operasional klien | Belum dikerjakan | Memerlukan Agent adapter write-back terotorisasi; tidak boleh dianggap selesai dari implementasi Gateway ini |
| Backfill snapshot untuk audit log lama | Belum dikerjakan | Harus berupa job terpisah dengan label `legacy_backfill`, bukan dianggap snapshot sezaman |
| Smoke test MinIO end-to-end | Menunggu lingkungan | Jalankan setelah Docker Engine dan kredensial lokal tersedia |

---

## 2. Tujuan

Membangun mekanisme recovery yang memungkinkan AuditChain Gateway:

1. menyimpan setiap audit log pada PostgreSQL sebagai data operasional;
2. menyimpan snapshot asli setiap versi ke MinIO secara otomatis;
3. memastikan snapshot lama tidak dapat diedit atau dihapus selama masa retensi;
4. mendeteksi perubahan ilegal pada audit log PostgreSQL;
5. menampilkan versi valid yang dapat dipilih untuk recovery;
6. memverifikasi snapshot terpilih terhadap hash, Merkle Proof, dan anchor Fabric;
7. memulihkan data tanpa menghapus riwayat insiden dan aktivitas recovery;
8. tetap dapat menerima log ketika MinIO mengalami gangguan sementara melalui mekanisme outbox dan retry;
9. menyediakan deployment lokal yang dapat direproduksi ke staging dan production menggunakan Docker Compose.

---

## 3. Batasan Ruang Lingkup

### 3.1 Termasuk dalam implementasi backend

- Penggantian service immudb menjadi MinIO pada Docker Compose.
- MinIO bucket initialization, versioning, Object Lock, retention, dan IAM policy.
- MinIO client pada aplikasi Go.
- Enkripsi snapshot sebelum penyimpanan.
- Transactional outbox untuk pengiriman snapshot.
- Snapshot worker dengan retry dan idempotensi.
- Penambahan status snapshot pada audit log.
- Tabel `snapshot_outbox`, `tamper_incidents`, dan `recovery_requests`.
- Integrasi snapshot dengan pipeline hash, Merkle, dan Fabric.
- Endpoint backend untuk insiden, daftar versi, permintaan recovery, persetujuan, dan eksekusi.
- Audit trail untuk setiap proses recovery.
- Backfill log lama yang masih dapat diverifikasi.
- Unit test, integration test, failure test, dan end-to-end test.
- Dokumentasi konfigurasi, deployment, monitoring, backup, dan rollback.

### 3.2 Tidak langsung termasuk

- Implementasi tampilan menu Recovery pada repository frontend yang berbeda.
- Recovery langsung ke database operasional milik klien tanpa mekanisme Agent yang terotorisasi.
- MinIO multi-node distributed cluster pada tahap lokal.
- Migrasi data lokal menjadi data production.
- Penghapusan otomatis data medis tanpa kebijakan retensi dan persetujuan legal.

### 3.3 Batas recovery versi pertama

Recovery versi pertama memulihkan salinan audit yang berubah di `audit_logs`. Recovery terhadap record asli pada database klien harus menjadi fase terpisah karena Gateway saat ini menerima CDC, tetapi belum memiliki jalur write-back generik dan aman ke database klien.

Jika recovery ke database klien menjadi kebutuhan wajib, jalurnya harus melalui Agent dengan:

- endpoint recovery terautentikasi;
- allowlist tabel dan kolom;
- persetujuan pengguna;
- dry-run;
- optimistic locking;
- idempotency key;
- event CDC baru setelah recovery.

---

## 4. Kondisi Repository Saat Dokumen Dibuat

### 4.1 Kondisi yang sudah tersedia

- Backend menggunakan Go 1.25, Gin, GORM, dan PostgreSQL.
- CDC aktif melalui Kafka/Debezium.
- Kafka consumer membuat `AuditLog`, menghitung hash SHA3-256, lalu menyimpannya dengan status `HASHED`.
- Aggregator saat ini mengambil semua log berstatus `HASHED` tanpa memeriksa ketersediaan snapshot recovery.
- Merkle Root dikirim ke Hyperledger Fabric.
- Verifikasi integritas log dan Merkle/Fabric sudah tersedia pada modul audit.
- Docker Compose sudah menjalankan PostgreSQL dan API Gateway.
- Workflow deployment dev berjalan ketika branch `ops-Petrus` menerima push.
- `deploy.sh` menjalankan `docker compose up -d --build`.

### 4.2 Kondisi yang belum tersedia

- Belum ada MinIO client pada `go.mod`.
- Belum ada model snapshot/outbox, insiden tamper, atau recovery request.
- Belum ada snapshot worker.
- Belum ada API recovery.
- Belum ada gating agar log hanya di-anchor setelah snapshot aman.
- Belum ditemukan test suite otomatis untuk pipeline yang ada.
- Belum ada kebijakan role khusus requester dan approver recovery.
- Belum ada monitoring terhadap backlog outbox dan kegagalan MinIO.

### 4.3 Perubahan immudb yang perlu ditangani

Branch aktif sudah memiliki dua commit konfigurasi immudb:

- `fb494ed` — penambahan service immudb pada Docker Compose;
- `4ad6fe0` — penambahan port Web Console immudb.

Implementasi MinIO tidak boleh ditambahkan berdampingan tanpa keputusan. Pada tahap pertama, konfigurasi immudb harus diganti dengan MinIO agar tidak ada dua recovery store, port yang tidak diperlukan, dependency yang membingungkan, atau volume yatim yang dianggap masih digunakan.

Volume immudb lama tidak akan dihapus otomatis selama fase pengembangan. Penghapusan volume hanya dilakukan setelah dipastikan tidak mengandung data yang perlu dipertahankan dan setelah mendapatkan persetujuan eksplisit.

### 4.4 Catatan branch deployment

Branch lokal `ops-Petrus` sudah tidak memiliki upstream remote, sedangkan workflow deployment masih memantau `ops-Petrus`. Sebelum staging, harus diputuskan salah satu:

1. membuat kembali branch remote `ops-Petrus` dan menggunakannya sebagai branch deployment; atau
2. mengubah workflow agar memantau branch integrasi yang benar, misalnya `dev`.

Tidak boleh melakukan push production sebelum ketidaksesuaian branch ini diselesaikan.

---

## 5. Keputusan Arsitektur

### 5.1 Pembagian tanggung jawab

| Komponen | Tanggung jawab |
|---|---|
| Database klien | Menyimpan data operasional milik klien |
| Debezium/Kafka | Mengirim perubahan data sebagai CDC event |
| PostgreSQL Gateway | Menyimpan audit log, indeks snapshot, insiden, permintaan recovery, dan outbox |
| MinIO | Menyimpan snapshot terenkripsi yang append-only/WORM |
| Hyperledger Fabric | Menyimpan anchor Merkle Root sebagai bukti integritas independen |
| Recovery Service | Memverifikasi dan mengeksekusi recovery yang disetujui |

### 5.2 Prinsip penyimpanan

- PostgreSQL tetap menjadi sumber query operasional.
- MinIO menjadi sumber payload recovery.
- Fabric tetap menjadi sumber bukti anchor, bukan tempat menyimpan payload.
- MinIO tidak melakukan sinkronisasi dua arah dengan PostgreSQL.
- Perubahan langsung pada PostgreSQL tidak boleh memicu penimpaan snapshot MinIO.
- Satu audit event menghasilkan satu object key unik.
- Snapshot lama tidak diperbarui di tempat.
- Recovery menghasilkan audit event baru dan tidak menghapus histori insiden.

### 5.3 Topologi target

```text
Database Klien
      |
      | CDC
      v
Debezium -> Kafka
              |
              v
      AuditChain Gateway
              |
              | satu transaksi PostgreSQL
              +---- audit_logs
              +---- snapshot_outbox
              +---- kafka_offsets
                        |
                        v
                 Snapshot Worker
                        |
                        v
                 MinIO Recovery Vault
                        |
                        | snapshot_status = VERIFIED
                        v
                 Merkle Aggregator
                        |
                        v
               Hyperledger Fabric
```

---

## 6. Alur Sistem Target

### 6.1 Alur event normal

1. Database klien menerima `INSERT`, `UPDATE`, atau `DELETE` yang sah.
2. Debezium menerbitkan CDC event ke Kafka.
3. Kafka consumer menormalisasi payload dan membuat metadata canonical.
4. Gateway menghitung `hash_value` dari audit log.
5. Dalam satu transaksi PostgreSQL, Gateway membuat:
   - row `audit_logs`;
   - row `snapshot_outbox` dengan payload canonical yang sama;
   - row `kafka_offsets`.
6. Snapshot worker mengambil outbox berstatus `PENDING`.
7. Worker membentuk snapshot envelope dan mengenkripsinya.
8. Worker mengunggah snapshot ke object key unik di MinIO.
9. Worker membaca kembali metadata object atau melakukan `HEAD` untuk memastikan object tersedia.
10. Gateway menyimpan `object_key`, `version_id`, checksum, dan waktu penyimpanan.
11. `snapshot_status` berubah menjadi `VERIFIED`.
12. Aggregator hanya mengambil log yang `status = HASHED` dan `snapshot_status = VERIFIED`.
13. Merkle Root dibuat dan di-anchor ke Fabric.

### 6.2 Alur ketika MinIO sementara gagal

1. Audit log dan outbox tetap tersimpan dalam transaksi PostgreSQL.
2. Outbox berubah menjadi `RETRY` dan mencatat error terakhir.
3. Worker melakukan exponential backoff dengan jitter.
4. Log tidak boleh masuk batch Merkle selama snapshot belum `VERIFIED`.
5. Setelah MinIO pulih, worker mengunggah snapshot menggunakan idempotency key yang sama.
6. Setelah snapshot terverifikasi, pipeline Merkle dilanjutkan.

Kebijakan ini memilih konsistensi recovery dibanding kecepatan anchoring. Alert harus aktif jika backlog melewati ambang batas.

### 6.3 Alur tamper

Contoh kondisi awal:

```text
L2 | Data_RM:123 | UPDATE | sakit demam | HASHED/ANCHORED
```

Snapshot MinIO:

```text
object_key  = prod/<client>/<date>/<log_id>/<hash>.snapshot
version_id  = <MinIO Version ID>
payload     = sakit demam (di dalam ciphertext)
```

Jika penyusup mengubah PostgreSQL menjadi `sakit pinggang`:

1. Snapshot MinIO tidak ikut berubah.
2. Verifier menghitung ulang hash row PostgreSQL.
3. Hash aktual tidak cocok dengan `hash_value` dan bukti Fabric.
4. Sistem membuat satu `tamper_incidents` yang idempoten untuk log tersebut.
5. Insiden muncul pada API/menu Recovery.

### 6.4 Alur recovery

1. User membuka insiden tamper.
2. Sistem menampilkan histori versi valid berdasarkan `resource` dan snapshot status.
3. User memilih snapshot yang tervalidasi. Pada MVP Gateway, snapshot yang dapat dieksekusi harus berasal dari `log_id` target yang sama; pemilihan snapshot lintas event untuk menulis kembali record database klien menunggu Agent adapter.
4. Sistem membuat `recovery_requests` dengan status `PENDING_APPROVAL`.
5. Approver yang berwenang menyetujui request.
6. Recovery executor mengambil object dengan pasangan tepat:
   - `object_key`;
   - `version_id`.
7. Snapshot didekripsi.
8. Hash plaintext dihitung ulang.
9. Hash diverifikasi terhadap:
   - `audit_logs.hash_value` versi terpilih;
   - Merkle Proof;
   - Merkle Root pada Fabric.
10. Executor mengunci row target PostgreSQL untuk mencegah race condition.
11. Executor menyimpan nilai tampered pada detail insiden untuk forensik.
12. Executor memulihkan field canonical dari snapshot.
13. Executor memverifikasi kembali hasil pemulihan.
14. Sistem menyimpan nilai tampered untuk forensik dan membuat audit event recovery baru.
15. `recovery_requests` menjadi `SUCCEEDED` dan `tamper_incidents` menjadi `RESOLVED`.

Eksekusi recovery ditolak jika snapshot writer/outbox belum aktif, karena recovery wajib menghasilkan audit event baru dan snapshot audit event tersebut.

Jika verifikasi gagal, tidak ada perubahan data dan request menjadi `FAILED_VERIFICATION`.

---

## 7. Desain MinIO

### 7.1 Service lokal

Docker Compose akan memiliki:

```text
postgres-db
minio
minio-init
api-gateway
```

Ketentuan:

- image MinIO dan MinIO Client harus memakai versi/tag yang dipin, bukan `latest`;
- data MinIO menggunakan named volume `minio-data`;
- API MinIO hanya tersedia pada network internal Compose untuk Gateway;
- console boleh dipublikasikan hanya pada lokal/staging dan tidak dibuka publik di production;
- health check wajib;
- `api-gateway` tidak menggunakan root credential MinIO;
- `minio-init` bersifat idempoten dan berhenti setelah bootstrap selesai.

### 7.2 Bucket

Nama sementara:

```text
auditchain-recovery
```

Konfigurasi:

| Environment | Versioning | Object Lock | Mode | Retention awal |
|---|---|---|---|---|
| Local | Aktif | Aktif | Governance | Pendek untuk pengujian |
| Staging | Aktif | Aktif | Governance | Sesuai kebutuhan pengujian |
| Production | Aktif | Aktif | Compliance | Ditentukan kebijakan bisnis/legal |

Retention production tidak boleh ditentukan hanya oleh developer. Nilainya harus disetujui pemilik data karena Compliance Mode membatasi penghapusan sampai masa retensi berakhir.

### 7.3 Object key

Object key tidak boleh mengandung nama pasien, diagnosis, email, nomor rekam medis mentah, atau PII lainnya.

Format yang disarankan:

```text
<environment>/<client-id>/<yyyy>/<mm>/<dd>/<log-id>/<hash>.snapshot
```

Contoh:

```text
prod/client-uuid/2026/09/17/L2/abc123.snapshot
```

Walaupun object key unik, `version_id` tetap disimpan untuk memastikan recovery membaca versi yang tepat dan tidak bergantung pada object versi `latest`.

### 7.4 Snapshot envelope

Plaintext sebelum enkripsi:

```json
{
  "schema_version": 1,
  "log_id": "L2",
  "client_id": "client-uuid",
  "actor": "Indah",
  "action": "UPDATE",
  "resource": "Data_RM:123",
  "timestamp": "2026-09-17T10:00:00Z",
  "source_system": "hospital-system",
  "authorization_context": "",
  "metadata": {
    "diagnosis": "sakit demam"
  },
  "hash_algorithm": "SHA3-256",
  "hash_value": "abc123"
}
```

Aturan:

- JSON harus canonical dan deterministik;
- hash audit dihitung dari plaintext canonical sesuai formula proyek;
- snapshot dienkripsi sebelum upload;
- object metadata MinIO tidak boleh berisi metadata medis;
- `schema_version` digunakan agar snapshot lama tetap dapat dibaca setelah struktur berkembang.

### 7.5 Enkripsi

Minimum:

- TLS untuk koneksi Gateway ke MinIO di staging/production;
- enkripsi payload menggunakan authenticated encryption;
- key ID disimpan bersama envelope/header, tetapi material key tidak disimpan di PostgreSQL atau MinIO;
- dukungan rotasi key tanpa membuat snapshot lama tidak dapat dibaca;
- credential dan encryption key tidak boleh masuk repository.

Keputusan implementasi yang direkomendasikan:

- AES-256-GCM untuk application-level encryption;
- nonce unik per object;
- konfigurasi `SNAPSHOT_ENCRYPTION_ACTIVE_KEY_ID`;
- keyring yang dapat membaca key lama;
- production key berasal dari secret manager atau file secret berpermission ketat.

---

## 8. Desain Database

### 8.1 Perubahan `audit_logs`

Kolom tambahan yang direncanakan:

| Kolom | Tujuan |
|---|---|
| `snapshot_status` | `PENDING`, `UPLOADING`, `VERIFIED`, `RETRY`, `FAILED`, `LEGACY_MISSING` |
| `snapshot_object_key` | Object key MinIO |
| `snapshot_version_id` | Version ID yang harus digunakan saat recovery |
| `snapshot_checksum` | Checksum ciphertext/object |
| `snapshot_plaintext_hash` | Hash canonical payload untuk verifikasi |
| `snapshot_stored_at` | Waktu upload berhasil |
| `snapshot_verified_at` | Waktu object diverifikasi |
| `snapshot_last_error` | Error terakhir tanpa menyimpan secret |

Index minimum:

- `(client_id, resource, timestamp)`;
- `(snapshot_status, timestamp)`;
- unique constraint pada `snapshot_object_key` jika sesuai hasil proof of concept.

### 8.2 Tabel `snapshot_outbox`

Tabel teknis ini memastikan audit log dan pekerjaan upload dibuat dalam satu transaksi.

Kolom minimum:

| Kolom | Tujuan |
|---|---|
| `id` | UUID primary key |
| `log_id` | Referensi audit log |
| `client_id` | Isolasi tenant |
| `event_type` | `STORE_AUDIT_SNAPSHOT` |
| `payload` | Salinan canonical/encrypted sementara yang akan dikirim |
| `payload_hash` | Hash plaintext canonical |
| `status` | `PENDING`, `PROCESSING`, `RETRY`, `COMPLETED`, `DEAD_LETTER` |
| `attempt_count` | Jumlah percobaan |
| `next_attempt_at` | Jadwal retry |
| `locked_at` | Lease worker |
| `locked_by` | Identitas worker |
| `last_error` | Error yang sudah disanitasi |
| `created_at` | Waktu pembuatan |
| `processed_at` | Waktu selesai |

Worker harus menggunakan row locking seperti `FOR UPDATE SKIP LOCKED` agar aman jika nanti ada lebih dari satu instance Gateway.

### 8.3 Tabel `tamper_incidents`

Kolom minimum:

| Kolom | Tujuan |
|---|---|
| `id` | UUID primary key |
| `client_id` | Isolasi tenant |
| `log_id` | Audit log yang terindikasi tamper |
| `resource` | Resource terkait |
| `incident_type` | Misalnya `METADATA_HASH_MISMATCH` |
| `expected_hash` | Hash yang sah |
| `detected_hash` | Hash hasil perhitungan dari row saat terdeteksi |
| `tampered_payload` | Salinan terbatas/terenkripsi untuk bukti forensik |
| `status` | `OPEN`, `RESOLVED` |
| `detected_at` | Waktu deteksi |
| `resolved_at` | Waktu penyelesaian |

Harus ada unique constraint untuk mencegah insiden aktif ganda pada kombinasi log dan jenis insiden yang sama.

### 8.4 Tabel `recovery_requests`

Kolom minimum:

| Kolom | Tujuan |
|---|---|
| `id` | UUID primary key |
| `client_id` | Isolasi tenant |
| `incident_id` | Insiden asal |
| `target_log_id` | Row yang akan dipulihkan |
| `selected_log_id` | Versi snapshot pilihan user |
| `snapshot_object_key` | Snapshot yang dipilih |
| `snapshot_version_id` | Versi object yang dipilih |
| `requested_by` | User requester |
| `approved_by` | User approver |
| `executed_by` | User executor |
| `reason` | Alasan recovery |
| `status` | State machine recovery |
| `idempotency_key` | Mencegah eksekusi ganda |
| `before_hash` | Hash sebelum recovery |
| `after_hash` | Hash setelah recovery |
| `failure_reason` | Error tersanitasi |
| `requested_at` | Waktu request |
| `approved_at` | Waktu approval |
| `executed_at` | Waktu eksekusi |

State machine:

```text
PENDING_APPROVAL
    -> APPROVED
    -> EXECUTING
    -> SUCCEEDED

PENDING_APPROVAL -> REJECTED
APPROVED/EXECUTING -> FAILED_VERIFICATION
APPROVED/EXECUTING -> FAILED_EXECUTION
```

---

## 9. Modul Backend yang Direncanakan

Struktur awal:

```text
internal/
  config/
    minio.go
    snapshot_crypto.go
  models/
    snapshot_outbox.go
    tamper_incident.go
    recovery_request.go
  modules/
    recovery/
      router.go
      handler.go
      service.go
      repository.go
      dto.go
  engine/
    snapshotworker/
      worker.go
      retry.go
      serializer.go
  storage/
    snapshotstore/
      interface.go
      minio.go
      fake.go
```

Prinsip:

- service recovery bergantung pada interface `SnapshotStore`, bukan langsung pada SDK MinIO;
- fake/in-memory store digunakan untuk unit test;
- handler tidak boleh mengandung logika enkripsi, verifikasi, atau query langsung;
- semua query harus selalu memfilter `client_id`, kecuali endpoint admin yang memang lintas tenant;
- log aplikasi tidak boleh mencetak payload medis, secret, ciphertext, atau encryption key.

---

## 10. Rencana API

Prefix yang disarankan:

```text
/api/dashboard/recovery
```

| Method | Endpoint | Fungsi | Role awal |
|---|---|---|---|
| `GET` | `/incidents` | Daftar insiden tenant | Auditor/Admin |
| `GET` | `/incidents/:id` | Detail insiden | Auditor/Admin |
| `GET` | `/resources/:resource/versions` | Daftar snapshot valid | Auditor/Admin |
| `GET` | `/requests` | Daftar request recovery | Auditor/Admin |
| `GET` | `/requests/:id` | Detail request recovery | Auditor/Admin |
| `POST` | `/requests` | Membuat permintaan recovery | Auditor/Admin |
| `GET` | `/requests/:id` | Status request | Requester/Admin |
| `POST` | `/requests/:id/approve` | Menyetujui recovery | Admin/Recovery Approver |
| `POST` | `/requests/:id/reject` | Menolak recovery | Admin/Recovery Approver |
| `POST` | `/requests/:id/execute` | Menjalankan request yang disetujui | Admin/Recovery Executor |

Aturan API:

- semua mutasi menerima idempotency key;
- requester tidak boleh menyetujui request miliknya sendiri jika four-eyes approval diterapkan;
- response versi tidak mengirim payload medis lengkap sebelum user memiliki otorisasi;
- preview harus dimasking sesuai kebutuhan;
- eksekusi harus memverifikasi ulang snapshot walaupun request sebelumnya sudah diverifikasi;
- Swagger diperbarui setelah kontrak endpoint stabil.

---

## 11. Konfigurasi Environment

Nama awal yang direncanakan:

```env
RECOVERY_ENABLED=false
SNAPSHOT_WRITER_ENABLED=false
SNAPSHOT_REQUIRED_FOR_ANCHOR=true

MINIO_ENDPOINT=minio:9000
MINIO_USE_TLS=false
MINIO_BUCKET=auditchain-recovery
MINIO_ACCESS_KEY=<runtime-writer-key>
MINIO_SECRET_KEY=<runtime-writer-secret>

MINIO_ROOT_USER=<bootstrap-only>
MINIO_ROOT_PASSWORD=<bootstrap-only>

SNAPSHOT_ENCRYPTION_ACTIVE_KEY_ID=key-2026-01
SNAPSHOT_ENCRYPTION_KEY=<base64-or-hex-32-byte-secret>
SNAPSHOT_WORKER_CONCURRENCY=2
SNAPSHOT_MAX_ATTEMPTS=10
SNAPSHOT_RETRY_BASE_SECONDS=5
SNAPSHOT_POLL_INTERVAL_SECONDS=2
```

Catatan:

- `.env.example` hanya berisi nama variable dan nilai dummy;
- `.env` lokal dan production tidak boleh di-commit;
- root credential hanya digunakan initializer/admin, tidak diberikan ke Gateway;
- nama variable keyring final harus dirancang agar kompatibel dengan environment variable dan secret manager.
- `SNAPSHOT_REQUIRED_FOR_ANCHOR=true` wajib disertai writer aktif; Gateway menolak start jika kombinasi ini tidak terpenuhi.

---

## 12. Keamanan

### 12.1 Kontrol wajib

- Versioning aktif.
- Object Lock aktif sejak provisioning bucket.
- Production menggunakan Compliance retention setelah kebijakan disetujui.
- Writer tidak memiliki izin delete atau mengubah retention.
- Reader recovery tidak memiliki izin put atau delete.
- Root credential tidak digunakan aplikasi.
- MinIO tidak diekspos langsung ke internet.
- TLS aktif di staging/production.
- Payload dienkripsi sebelum upload.
- Exact `version_id` digunakan saat read recovery.
- Hash diverifikasi setelah download dan sebelum write recovery.
- Tenant isolation diuji untuk seluruh endpoint.
- Recovery menggunakan approval dan audit trail.
- Secret tidak muncul di log atau response API.

### 12.2 Ancaman dan mitigasi

| Ancaman | Mitigasi |
|---|---|
| SQL injection/akses DB mengubah metadata | Hash mismatch, Fabric proof, snapshot MinIO terpisah |
| Penyerang menulis object dengan key sama | Object key unik, versioning, exact version ID, verifikasi hash |
| Penyerang menghapus object | Object Lock, deny delete, Compliance retention |
| Credential Gateway bocor | Least privilege, rotasi credential, network restriction |
| MinIO disk rusak | Backup/replication ke media atau server berbeda |
| Worker mengunggah dua kali | Deterministic object key dan idempotent outbox |
| Recovery dijalankan dua kali | Idempotency key dan state machine transaction |
| User lintas tenant membaca snapshot | Filter `client_id` dan authorization test |
| Payload medis terbaca admin storage | Application-level encryption dan key terpisah |

Object Lock bukan pengganti backup. Single-node MinIO tetap memiliki risiko kehilangan disk/server sehingga production membutuhkan salinan terpisah.

---

## 13. Tahapan Implementasi

### Fase 0 — Persetujuan desain dan baseline

Estimasi: 1 hari kerja.

Pekerjaan:

- Setujui ruang lingkup recovery versi pertama.
- Putuskan target recovery: hanya `audit_logs` atau juga database klien.
- Putuskan role requester, approver, dan executor.
- Putuskan retention local, staging, dan production.
- Putuskan branch integrasi dan branch deployment.
- Simpan hasil baseline test/build sebelum perubahan.
- Pastikan perubahan immudb tidak berisi data yang perlu dimigrasikan.

Deliverable:

- Keputusan desain tercatat.
- Baseline build/test tercatat.
- Tidak ada keputusan kritis yang dibiarkan implisit.

Exit criteria:

- Seluruh keputusan pada Bagian 19 berstatus `DECIDED`.

### Fase 1 — MinIO lokal dan bootstrap

Estimasi: 2 hari kerja.

Pekerjaan:

- Ganti service immudb di Docker Compose dengan MinIO.
- Tambahkan persistent volume MinIO.
- Tambahkan health check.
- Tambahkan `minio-init` idempoten.
- Buat bucket dengan versioning dan Object Lock.
- Buat policy writer dan reader terpisah.
- Hapus dependency `api-gateway -> immudb`.
- Tambahkan `.env.example` tanpa secret nyata.
- Pin versi image.

Deliverable:

- `docker compose up -d` menjalankan PostgreSQL, MinIO, initializer, dan Gateway.
- Bucket dapat diperiksa melalui CLI/console lokal.

Exit criteria:

- Restart Compose tidak menghilangkan object.
- Writer dapat menambah object tetapi tidak dapat menghapus object locked.
- Reader dapat membaca tetapi tidak dapat menulis.

### Fase 2 — MinIO client, snapshot format, dan enkripsi

Estimasi: 2 hari kerja.

Pekerjaan:

- Tambahkan dependency SDK S3/MinIO yang dipin.
- Buat interface `SnapshotStore`.
- Buat implementasi MinIO dan fake store.
- Definisikan snapshot schema version 1.
- Implementasikan canonical serialization.
- Implementasikan encrypt/decrypt envelope.
- Implementasikan deterministic object key.
- Tambahkan startup validation untuk konfigurasi wajib.

Deliverable:

- Library internal dapat put, head, dan get exact object version.
- Snapshot plaintext tidak muncul di MinIO.

Exit criteria:

- Unit test serialization deterministik lulus.
- Unit test encryption round-trip dan wrong-key failure lulus.
- Unit test exact-version read lulus.

### Fase 3 — Schema database dan migrasi

Estimasi: 1 hari kerja.

Pekerjaan:

- Tambahkan kolom snapshot pada `AuditLog`.
- Tambahkan model `SnapshotOutbox`.
- Tambahkan model `TamperIncident`.
- Tambahkan model `RecoveryRequest`.
- Tambahkan index dan unique constraint.
- Evaluasi AutoMigrate untuk local dan migration eksplisit untuk staging/production.
- Buat rollback yang hanya menonaktifkan fitur dan tidak menghapus data.

Deliverable:

- Schema tersedia pada database kosong dan database yang sudah berisi data.

Exit criteria:

- Migrasi forward lulus.
- Aplikasi lama/fitur non-recovery tetap dapat membaca data.
- Tidak ada destructive migration.

### Fase 4 — Transactional outbox pada jalur CDC

Estimasi: 2 hari kerja.

Pekerjaan:

- Refactor `processMessage` agar pembuatan audit log, outbox, dan Kafka offset berada dalam satu transaksi.
- Simpan canonical payload yang sama ke outbox.
- Pastikan kegagalan salah satu insert me-rollback seluruh transaksi.
- Pertahankan deduplikasi Kafka yang sudah ada.
- Pastikan retry Kafka tidak membuat outbox ganda.
- Hilangkan logging raw payload medis dari mode normal atau lindungi dengan debug flag aman.

Deliverable:

- Setiap audit log baru selalu memiliki tepat satu pekerjaan snapshot.

Exit criteria:

- Tidak ada audit log baru tanpa outbox.
- Tidak ada outbox orphan tanpa audit log.
- Duplikasi Kafka menghasilkan maksimal satu log dan satu outbox.

### Fase 5 — Snapshot worker dan gating Merkle

Estimasi: 3 hari kerja.

Pekerjaan:

- Implementasikan worker polling menggunakan row lease/locking.
- Implementasikan upload idempoten.
- Simpan object key, version ID, checksum, dan timestamps.
- Implementasikan retry, jitter, dan dead-letter.
- Implementasikan graceful shutdown.
- Tambahkan health/readiness status untuk worker.
- Ubah aggregator agar hanya mengambil log dengan snapshot `VERIFIED`.
- Tambahkan metrik backlog dan failure.

Deliverable:

- Pipeline CDC -> PostgreSQL -> MinIO -> Merkle -> Fabric berjalan end-to-end.

Exit criteria:

- Gangguan MinIO tidak menghilangkan audit event.
- Log tanpa snapshot tidak di-anchor.
- Setelah MinIO pulih, backlog selesai otomatis.
- Restart worker tidak membuat object duplikat yang tidak terkendali.

### Fase 6 — Deteksi tamper dan incident lifecycle

Estimasi: 2 hari kerja.

Pekerjaan:

- Integrasikan hasil verifikasi audit dengan pembuatan incident.
- Buat incident idempoten.
- Simpan expected hash dan detected hash.
- Tambahkan query/filter incident per tenant dan status.
- Tambahkan endpoint daftar dan detail incident.
- Pastikan on-demand verification dan scheduled verification menggunakan service yang sama.

Deliverable:

- Perubahan langsung pada metadata PostgreSQL menghasilkan incident `OPEN`.

Exit criteria:

- Verifikasi berulang tidak membuat incident duplikat.
- Tenant lain tidak dapat melihat incident.
- Payload sensitif tidak bocor melalui log atau response.

### Fase 7 — Recovery request dan executor

Estimasi: 3 hari kerja.

Pekerjaan:

- Implementasikan daftar versi recovery.
- Implementasikan create/approve/reject request.
- Implementasikan state machine dan idempotency key.
- Implementasikan exact object version download.
- Implementasikan decrypt dan hash verification.
- Implementasikan Merkle/Fabric verification sebelum recovery.
- Implementasikan row lock dan safe update.
- Simpan bukti nilai tampered untuk forensik.
- Buat recovery audit event baru dan simpan nilai tampered untuk forensik.
- Resolve incident setelah post-recovery verification berhasil.

Deliverable:

- User dapat memilih snapshot target yang tervalidasi dan memulihkan audit log target. Pemulihan V1/V2/V3 lintas event ke database klien menjadi deliverable Agent adapter.

Exit criteria:

- Snapshot dengan hash salah selalu ditolak.
- Request belum disetujui tidak dapat dieksekusi.
- Eksekusi ulang dengan idempotency key yang sama tidak mengubah data dua kali.
- Riwayat sebelum, insiden, dan hasil recovery tetap tersedia.

### Fase 8 — Legacy backfill

Estimasi: 1 hari kerja.

Pekerjaan:

- Klasifikasikan log lama menjadi anchored-valid, pending, tampered, atau unverifiable.
- Verifikasi ulang log anchored terhadap Merkle/Fabric sebelum membuat snapshot.
- Backfill hanya log yang lolos verifikasi.
- Tandai sumber sebagai `LEGACY_BACKFILL`.
- Jangan menyatakan snapshot backfill dibuat pada waktu event asli.
- Log yang tidak dapat diverifikasi diberi status `LEGACY_MISSING` dan dilaporkan.

Deliverable:

- Laporan jumlah log berhasil, gagal, dan tidak dapat di-backfill.

Exit criteria:

- Tidak ada klaim bahwa snapshot backfill adalah bukti sezaman dengan event lama.
- Kegagalan satu log tidak menghentikan seluruh batch.

### Fase 9 — Hardening, dokumentasi, dan staging

Estimasi: 3 hari kerja.

Pekerjaan:

- Jalankan seluruh test matrix.
- Jalankan threat/failure scenarios.
- Perbarui Swagger dan README.
- Dokumentasikan backup dan restore MinIO.
- Dokumentasikan key rotation.
- Dokumentasikan incident response.
- Selesaikan branch deployment.
- Deploy staging dengan feature flag disabled.
- Jalankan migration dan smoke test.
- Aktifkan writer, lalu gating, kemudian recovery secara bertahap.
- Observasi minimal satu hari kerja sebelum production.

Deliverable:

- Release candidate yang dapat di-deploy ulang menggunakan Docker Compose.

Exit criteria:

- Semua acceptance criteria lulus.
- Tidak ada secret di Git/image/log.
- Rollback drill berhasil tanpa menghapus snapshot.

### Fase 10 — Production rollout

Estimasi: 1 hari eksekusi dan minimal 2 hari observasi.

Urutan:

1. Backup PostgreSQL dan verifikasi restore point.
2. Siapkan storage/volume MinIO production.
3. Deploy MinIO dan bucket policy.
4. Verifikasi Object Lock dan retention.
5. Deploy schema additive dengan fitur nonaktif.
6. Aktifkan snapshot writer.
7. Pantau outbox dan error.
8. Aktifkan snapshot-required gating untuk log baru.
9. Aktifkan API incident.
10. Aktifkan recovery setelah smoke test approval flow.

Tidak boleh langsung mengaktifkan seluruh fitur sekaligus.

---

## 14. Timeline

Estimasi berikut mengasumsikan satu developer backend fokus, akses lokal tersedia, Fabric dapat digunakan untuk pengujian, dan keputusan pada Fase 0 tidak tertunda.

### 14.1 Timeline backend

| Minggu | Hari | Fokus | Output utama |
|---|---:|---|---|
| Minggu 1 | 1 | Persetujuan desain dan baseline | Scope, retention, role, branch final |
| Minggu 1 | 2-3 | MinIO Compose dan bootstrap | Bucket locked, policy writer/reader |
| Minggu 1 | 4-5 | Client, schema snapshot, enkripsi | Snapshot store tervalidasi |
| Minggu 2 | 6 | Schema PostgreSQL | Tabel dan index recovery |
| Minggu 2 | 7-8 | Transactional outbox | Audit log dan outbox atomik |
| Minggu 2 | 9-10 | Snapshot worker | Upload, retry, idempotensi |
| Minggu 3 | 11 | Merkle gating dan metrik | Hanya snapshot verified di-anchor |
| Minggu 3 | 12-13 | Tamper incident | Insiden terdeteksi dan dapat di-query |
| Minggu 3 | 14-15 | Recovery request/executor | Pilih versi dan recovery tervalidasi |
| Minggu 4 | 16 | Legacy backfill | Laporan dan snapshot legacy valid |
| Minggu 4 | 17-18 | Security/failure/E2E tests | Bukti pengujian lengkap |
| Minggu 4 | 19 | Staging rollout | Release candidate dan smoke test |
| Minggu 4 | 20 | Buffer/perbaikan | Stabilization sebelum production |

Total estimasi backend: **20 hari kerja atau sekitar 4 minggu**.

### 14.2 Timeline frontend

Frontend dapat mulai setelah kontrak API Fase 6 dan 7 stabil.

| Hari | Pekerjaan |
|---:|---|
| 1 | Halaman daftar incident dan filter status |
| 2 | Detail incident dan timeline versi |
| 3 | Pemilihan versi dan preview perbedaan |
| 4 | Request, approval, execute, dan progress state |
| 5 | Error handling, authorization, dan E2E UI |

Jika backend dan frontend dikerjakan paralel, target keseluruhan tetap sekitar 4 minggu. Jika dikerjakan oleh orang yang sama secara berurutan, tambahkan sekitar 5 hari kerja.

### 14.3 Faktor yang dapat menambah waktu

- Recovery wajib menulis kembali database klien melalui Agent: tambah sekitar 1-2 minggu.
- Fabric environment tidak stabil atau tidak tersedia.
- Kebijakan retention dan encryption key belum diputuskan.
- Perlu MinIO multi-node/replication sejak tahap awal.
- Perlu approval workflow lebih dari dua level.
- Struktur CDC DELETE tidak membawa data `before` yang cukup.

---

## 15. Test Matrix

### 15.1 Unit test

- Canonical JSON selalu menghasilkan byte yang sama.
- Hash snapshot sesuai hash audit.
- Encrypt/decrypt round-trip berhasil.
- Ciphertext yang dimodifikasi gagal diautentikasi.
- Object key deterministik dan tidak memuat PII.
- Retry schedule dan batas attempt benar.
- State machine recovery menolak transisi ilegal.
- Tenant authorization filter benar.

### 15.2 Integration test PostgreSQL dan MinIO

- Satu CDC event membuat audit log dan outbox.
- Outbox mengunggah tepat satu snapshot.
- Version ID dan checksum tercatat.
- Writer tidak dapat menghapus object locked.
- Reader tidak dapat menulis object.
- Exact-version read mengembalikan snapshot yang dipilih.
- Restart MinIO mempertahankan object.
- Restart worker melanjutkan outbox.

### 15.3 Failure test

- MinIO mati sebelum upload.
- MinIO mati setelah upload tetapi sebelum status PostgreSQL diperbarui.
- PostgreSQL transaction gagal setelah audit log dibuat.
- Worker mati saat status `PROCESSING`.
- Credential MinIO salah.
- Encryption key salah/hilang.
- Object tidak ditemukan.
- Version ID tidak ditemukan.
- Checksum atau ciphertext dimodifikasi.
- Fabric unreachable.
- Merkle Proof tidak cocok.

### 15.4 Tamper/recovery end-to-end

Skenario utama:

```text
1. User Indah membuat V1: sakit gigi.
2. User resmi membuat V2: sakit demam.
3. Pastikan V1 dan V2 tersimpan di PostgreSQL dan MinIO.
4. Pastikan V1/V2 di-anchor.
5. Ubah metadata L2 langsung melalui SQL menjadi sakit pinggang.
6. Jalankan verifier.
7. Pastikan incident OPEN terbentuk.
8. Pilih snapshot target V2 yang tervalidasi.
9. Buat dan setujui recovery request.
10. Jalankan recovery.
11. Pastikan metadata kembali menjadi sakit demam.
12. Pastikan hash dan Fabric verification kembali valid.
13. Pastikan recovery event baru tercatat.
14. Pastikan V1, V2, nilai tampered, incident, dan request tetap dapat diaudit.
```

Skenario pilihan versi:

- recovery snapshot target yang bukan object `latest`;
- penolakan pemilihan snapshot lintas `log_id` sebelum approval;
- penolakan snapshot yang tidak anchored;
- penolakan snapshot tenant lain;
- penolakan eksekusi tanpa approval;
- request yang sama dieksekusi dua kali.

### 15.5 Deployment test

- Deploy pada database kosong.
- Deploy pada database berisi log lama.
- Restart semua container.
- Upgrade Gateway tanpa restart MinIO bila memungkinkan.
- Rollback Gateway tanpa menghapus volume MinIO.
- Backup dan restore satu snapshot pada environment test.

---

## 16. Observability dan Operasional

### 16.1 Metrik minimum

- jumlah outbox `PENDING`;
- umur outbox tertua;
- upload success/failure rate;
- retry count;
- dead-letter count;
- snapshot verification failure;
- incident OPEN count;
- recovery success/failure count;
- durasi recovery;
- kapasitas MinIO;
- health PostgreSQL, MinIO, Fabric, dan worker.

### 16.2 Alert minimum

- MinIO unreachable lebih dari ambang waktu;
- backlog outbox terus meningkat;
- outbox tertua melewati SLA;
- dead-letter lebih dari nol;
- snapshot checksum mismatch;
- incident tamper baru;
- recovery gagal;
- storage hampir penuh;
- encryption key aktif tidak tersedia.

### 16.3 Log terstruktur

Field aman yang boleh dicatat:

```text
request_id
log_id
client_id
outbox_id
incident_id
recovery_request_id
object_key
version_id
status
attempt_count
duration_ms
```

Jangan mencatat metadata medis, token, password, secret key, encryption key, atau payload ciphertext penuh.

---

## 17. Strategi Deployment dan Rollback

### 17.1 Feature flag rollout

Tahapan aktivasi:

```text
RECOVERY_ENABLED=false
SNAPSHOT_WRITER_ENABLED=false
        |
        v
Deploy schema dan MinIO
        |
        v
SNAPSHOT_WRITER_ENABLED=true
        |
        v
Pantau upload dan backlog
        |
        v
SNAPSHOT_REQUIRED_FOR_ANCHOR=true
        |
        v
RECOVERY_ENABLED=true
```

### 17.2 Rollback aplikasi

Jika ada masalah:

1. nonaktifkan endpoint recovery;
2. nonaktifkan snapshot writer bila diperlukan;
3. pertahankan row outbox agar dapat diproses kembali;
4. rollback image Gateway;
5. jangan menghapus bucket, object, version, atau volume MinIO;
6. jangan drop kolom/tabel pada rollback darurat;
7. evaluasi backlog sebelum aktivasi kembali.

### 17.3 Data yang tidak boleh dihapus saat rollback

- `minio-data`;
- bucket `auditchain-recovery`;
- object versions;
- `snapshot_outbox`;
- `tamper_incidents`;
- `recovery_requests`;
- kolom referensi snapshot pada `audit_logs`.

---

## 18. Definition of Done

Implementasi dianggap selesai hanya jika seluruh kondisi berikut terpenuhi:

- [ ] immudb tidak lagi menjadi dependency runtime.
- [ ] MinIO dapat dijalankan melalui Docker Compose dengan image yang dipin.
- [ ] Bucket, versioning, Object Lock, retention, dan policy dibuat otomatis.
- [ ] Gateway tidak menggunakan root credential MinIO.
- [ ] Setiap audit log baru memiliki outbox dalam transaksi yang sama.
- [ ] Setiap log baru memiliki snapshot MinIO terverifikasi sebelum agregasi.
- [ ] Perubahan langsung di PostgreSQL tidak mengubah snapshot MinIO.
- [ ] Object lama tidak dapat dihapus oleh writer/recovery service.
- [ ] Exact version ID disimpan dan digunakan saat recovery.
- [ ] Payload MinIO terenkripsi dan object key tidak memuat PII.
- [ ] Tamper menghasilkan incident idempoten.
- [ ] User dapat memilih versi recovery.
- [ ] Recovery memerlukan approval sesuai keputusan role.
- [ ] Snapshot diverifikasi terhadap hash dan Fabric sebelum recovery.
- [ ] Recovery tidak menghapus riwayat dan menghasilkan audit event baru.
- [ ] MinIO outage diuji dan outbox berhasil retry.
- [ ] Tenant isolation test lulus.
- [ ] Test utama otomatis dan dapat dijalankan ulang.
- [ ] Swagger/README/deployment/runbook diperbarui.
- [ ] Backup dan rollback drill berhasil.
- [ ] Staging soak test selesai tanpa backlog/error kritis.

---

## 19. Keputusan yang Harus Dikunci Sebelum Coding

| ID | Keputusan | Rekomendasi awal | Status |
|---|---|---|---|
| D-01 | Target recovery versi pertama | Pulihkan `audit_logs`; client DB menjadi fase lanjutan | OPEN |
| D-02 | Retention local | Governance, durasi pendek | OPEN |
| D-03 | Retention staging | Governance sesuai masa pengujian | OPEN |
| D-04 | Retention production | Compliance berdasarkan kebijakan pemilik data | OPEN |
| D-05 | Approval | Auditor request, Admin approve/execute | OPEN |
| D-06 | Self-approval | Dilarang untuk production | OPEN |
| D-07 | Enkripsi | AES-256-GCM application-level | OPEN |
| D-08 | Pengelolaan production key | Secret manager atau mounted secret | OPEN |
| D-09 | Branch integrasi | `dev` atau branch fitur khusus | OPEN |
| D-10 | Branch deployment | Perbaiki `ops-Petrus` atau ubah workflow | OPEN |
| D-11 | Backfill legacy | Hanya log yang lolos verifikasi Fabric | OPEN |
| D-12 | SLA outbox | Tentukan batas umur backlog dan alert | OPEN |

Coding fitur inti dimulai setelah D-01, D-04, D-05, D-07, D-09, dan D-10 disetujui.

---

## 20. Urutan Pengerjaan yang Tidak Boleh Dilompati

```text
Persetujuan desain
    -> Baseline
    -> MinIO local/bootstrap
    -> Snapshot format + encryption
    -> Database schema
    -> Transactional outbox
    -> Snapshot worker
    -> MinIO failure/retry test
    -> Merkle gating
    -> Tamper incident
    -> Recovery request/approval
    -> Recovery executor
    -> E2E test
    -> Legacy backfill
    -> Staging
    -> Production
```

Aturan pelaksanaan:

- satu fase tidak dinyatakan selesai hanya karena kode sudah ditulis;
- exit criteria fase harus dibuktikan;
- kegagalan pada fase sebelumnya diperbaiki sebelum masuk fase berikutnya;
- tidak melakukan deploy production langsung dari hasil uji lokal;
- tidak menjalankan destructive cleanup terhadap volume lama tanpa persetujuan;
- perubahan di luar scope dicatat sebagai backlog dan tidak disisipkan diam-diam.

---

## 21. Referensi Teknis

- MinIO Object Lock dan immutability: <https://docs.min.io/aistor/administration/object-locking-and-immutability/>
- MinIO Object Versioning: <https://docs.min.io/aistor/administration/objects-and-versioning/versioning/>
- Dokumentasi API proyek: `README.md`, `DOCUMENTATION.md`, dan `docs/swagger.yaml`
- Pipeline aktif: `internal/engine/kafkaconsumer/consumer.go`, `internal/engine/aggregator/aggregator.go`, dan `internal/blockchain/fabric.go`
