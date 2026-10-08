# 🛡️ AuditChain Gateway

AuditChain Gateway adalah *Middleware* dan API Gateway berskala *Enterprise* yang menerima, memproses, dan mengunci log audit dari berbagai sistem klien (rumah sakit/SIMRS, data geospasial, dsb.) secara *immutable* (tidak dapat diubah) ke dalam jaringan **Hyperledger Fabric Blockchain**.

Sistem ini menggunakan arsitektur **multi-tenant (SaaS)**, ingestion via **Change Data Capture (CDC)** per klien, struktur data **Merkle Tree**, dan verifikasi berlapis untuk memastikan integritas data (Anti-Tampering) dengan performa *high-throughput*.

---

## ✨ Fitur Utama

- **🔌 Multi-Client CDC Ingestion:** Setiap klien memiliki konfigurasi Kafka sendiri (`ClientKafkaConfig`) — bukan Kafka terpusat. Gateway melakukan *reconciliation* otomatis setiap 15 detik untuk spawn/stop consumer goroutine per klien.
- **🌳 Merkle Tree Aggregation:** Menghemat biaya dan ruang *ledger* Blockchain dengan mengelompokkan log menjadi *batch*, membangun *proof chain* lengkap dari leaf ke root (mendukung batch berapa pun besarnya, termasuk kasus leaf ganjil/self-pairing), dan hanya mengirim *Merkle Root* ke jaringan Hyperledger Fabric.
- **🔗 Cryptographic Hashing (SHA3-256):** Setiap log dihitung hash-nya dari 8 field kanonik (actor, action, resource, timestamp, source system, authorization context, metadata, log ID). Local chain (`prevHash`) sudah tidak lagi menjadi bagian dari formula hash.
- **🕵️ 4-Layer Verification Engine:**
  1. **Layer 1** — Eksistensi log di database
  2. **Layer 2** — Re-hashing lokal terhadap PostgreSQL
  3. **Layer 3** — Verifikasi live ke *Universal Agent* yang berjalan di premise klien (`POST /verify/<table>/<id>`), hanya dijalankan untuk log **terbaru** per resource
  4. **Layer 4** — Rekonstruksi Merkle Proof dan pencocokan terhadap ledger Hyperledger Fabric
- **📊 Dashboard API:** Statistik, riwayat transaksi dengan pagination sungguhan, inventaris resource, verifikasi per-log/per-resource/per-range waktu, dan status integritas granular (`valid` / `tampered` / `pending` / `unreachable`) dengan detail `chain_issues` (mis. `merkle_mismatch`, `client_mismatch:<log_id>`). Status Gateway/Fabric (`integrity_status`/`chain_status`) dipisahkan dari konektivitas Agent (`agent_status`) dan status workflow recovery (`recovery_status`), sehingga Agent client yang offline tidak salah ditampilkan sebagai kerusakan snapshot.
- **📚 Interaktif API Docs:** Terintegrasi dengan **Swagger UI** untuk pengujian dan dokumentasi endpoint.

---

## 🏗️ Arsitektur Sistem

### Alur Data — Jalur Kafka CDC (jalur utama yang aktif)

```
[Database Klien] → Debezium CDC → [Kafka Broker milik klien]
        → Kafka Consumer per-klien (Gateway)
        → Hash langsung (SHA3-256) → PostgreSQL Gateway (status: HASHED)
```

Setiap klien (`ClientKafkaConfig`) punya broker, topic prefix, PK field, dan group ID sendiri. Consumer goroutine berjalan dengan context cancel per klien dan melakukan *reconciliation* berkala terhadap perubahan konfigurasi.

### Alur Data — Jalur HTTP Ingestion (tersedia, belum di-mount ke router)

```
[Client App] → POST /api/logs (API Key) → Normalisasi field dinamis
        → Redis Queue → Pipeline Worker (Hasher → Aggregator → Fabric Anchoring)
```

> ⚠️ **Catatan:** Modul `ingestion` sudah lengkap (handler, service, repository) namun route-nya **belum terdaftar** di `internal/api/router.go`. Saat ini seluruh ingestion produksi berjalan lewat jalur Kafka CDC.

### Pipeline Background (Ticker 10 detik)

1. **Hasher Engine** — `status=RECEIVED` → hitung SHA3-256 → `HASHED`
2. **Aggregator Engine** — kumpulkan log `HASHED` (batch 10) → bangun Merkle Tree + simpan `MerkleProof` per leaf → `AGGREGATED`
3. **Fabric Anchoring** — kirim Merkle Root unik ke chaincode `StoreMerkleRoot` → `ANCHORED` + `blockchain_tx_id`

### Status Lifecycle Log

```
RECEIVED → HASHED → AGGREGATED → ANCHORED
```
> Log yang masuk lewat Kafka CDC langsung menjadi `HASHED` (skip `RECEIVED`), karena hash dihitung inline saat message diproses.

---

## 🛠️ Tech Stack

- **Bahasa Pemrograman:** Go (Golang) 1.25
- **Web Framework:** Gin Web Framework
- **Database:** PostgreSQL (dengan GORM)
- **Message Broker:** Redis (jalur HTTP ingestion) & Apache Kafka + Debezium (jalur CDC, per-klien)
- **Blockchain:** Hyperledger Fabric v2.4+ (Gateway SDK)
- **Kriptografi:** SHA3-256 (Keccak)
- **Dokumentasi API:** Swaggo / Swagger
- **Agent Verifikasi Klien:** Repo terpisah (`auditchain-agent`), Go, mendukung Oracle (`go-ora`) dan PostgreSQL

---

## 🔐 Autentikasi

Sistem menggunakan tiga skema kredensial yang independen satu sama lain:

| Skema | Header | Kegunaan |
|---|---|---|
| **API Key** | `x-api-key` / `api-key` | Klien → Gateway (ingestion log) |
| **JWT Bearer** | `Authorization: Bearer <token>` | Dashboard/Auditor (2 jam expiry) |
| **verify_token** | `Authorization: Bearer <verify_token>` (disimpan di `agent_configs`) | Gateway → Universal Agent (Layer 3) |

`api_key` dan `verify_token` **tidak boleh tertukar** — arah otentikasinya berlawanan.

---

## 🚀 Instalasi & Konfigurasi

Pengelolaan schema PostgreSQL dipisahkan dari startup service. Runtime hanya
memeriksa schema secara read-only; migrasi dijalankan secara eksplisit dengan
perintah operator. Lihat [panduan migrasi database](docs/DATABASE_MIGRATION.md)
sebelum melakukan perubahan schema/data.

### Prasyarat
- Go 1.25+
- PostgreSQL
- Redis
- Akses ke Kafka broker milik masing-masing klien (untuk jalur CDC)
- Akses ke Node Hyperledger Fabric (Certificate, Private Key, MSP ID)

### Setup Environment

Buat file `.env` di *root* direktori:

```env
# Server
PORT=8080

# Database
DB_DSN=postgres://postgres:password@localhost:5433/test_blockchain?sslmode=disable

# Redis
REDIS_HOST=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# JWT
JWT_SECRET=ganti-dengan-secret-yang-kuat

# Admin
ADMIN_SECRET=ganti-dengan-secret-admin

# Hyperledger Fabric Gateway
FABRIC_MSP_ID=Org1MSP
FABRIC_PEER_ENDPOINT=localhost:7051
FABRIC_TLS_CERT_PATH=./crypto-config/tls/ca.crt
FABRIC_CERT_PATH=./crypto-config/users/Admin@org1/msp/signcerts/cert.pem
FABRIC_KEY_PATH=./crypto-config/users/Admin@org1/msp/keystore/priv_key.pem
FABRIC_CHANNEL=audit-channel
FABRIC_CHAINCODE=audit-contract

APP_ENV=local
GATEWAY_BACKGROUND_WORKERS_ENABLED=false
# Wajib dipilih eksplisit sebelum workers diaktifkan; berlaku hanya jika
# Kafka consumer group/partition belum memiliki committed offset.
KAFKA_CONSUMER_START_OFFSET=earliest
VERIFICATION_JOB_BATCH_SIZE=100
VERIFICATION_SCHEDULER_ENABLED=false
VERIFICATION_SCHEDULER_CLIENT_ID=
VERIFICATION_SCHEDULER_INTERVAL_SECONDS=86400
VERIFICATION_SCHEDULER_LOOKBACK_HOURS=24
VERIFICATION_SCHEDULER_OVERLAP_SECONDS=300
VERIFICATION_SCHEDULER_BATCH_SIZE=100
VERIFICATION_SCHEDULER_TIMEZONE=Asia/Jakarta
VERIFICATION_SCHEDULER_RUN_ON_START=false
RECOVERY_ENABLED=false
RECOVERY_MODE=agent_direct
RECOVERY_CDC_TIMEOUT_SECONDS=120
SNAPSHOT_WRITER_ENABLED=false
SNAPSHOT_REQUIRED_FOR_ANCHOR=false
SNAPSHOT_WORKER_CONCURRENCY=1
SNAPSHOT_MAX_ATTEMPTS=10
SNAPSHOT_RETRY_BASE_SECONDS=5
SNAPSHOT_POLL_INTERVAL_SECONDS=2
SNAPSHOT_ENCRYPTION_ACTIVE_KEY_ID=key-2026-01
SNAPSHOT_ENCRYPTION_KEY=ganti-dengan-key-32-byte-base64-atau-hex

# Optional only for the legacy snapshot compatibility path:
# RECOVERY_MODE=snapshot_legacy
# MINIO_ENDPOINT=minio:9000
# MINIO_BUCKET=auditchain-recovery
# MINIO_ACCESS_KEY=auditchain-writer
# MINIO_SECRET_KEY=ganti-dengan-secret-writer
# MINIO_USE_TLS=false
```

### Menjalankan Aplikasi

```bash
# 1. Clone repository
git clone <repo-url>

# 2. Unduh dependencies
go mod tidy

# 3. (Opsional) Generate ulang dokumentasi Swagger
swag init -g main.go

# 4. Jalankan server
go run main.go
```

Aplikasi berjalan di `http://localhost:8080` (atau sesuai `PORT`).
Swagger UI tersedia di `http://localhost:8080/swagger/index.html`.

### Menjalankan sebagai service terpisah di Mini PC

`gateway-runtime/docker-compose.yml` adalah Compose terpisah dari Compose
Agent di root repository. Menjalankannya tidak membangun ulang atau
menghentikan `auditchain-agent` pada port `9090`.

Di Mini PC:

```bash
cd ~/auditchain/auditchain-gateway-agent/gateway-runtime
cp .env.example .env
chmod 600 .env
```

Edit `.env` sebelum menjalankan service: isi DSN PostgreSQL Gateway (bukan DSN
read-only connectivity-check), alamat Tailscale Besu, secret JWT, origin
dashboard, serta parameter Fabric. Pastikan direktori `FABRIC_MATERIAL_DIR`
berisi `ca.crt`, `probe-cert.pem`, dan `probe-key.pem`; private key hanya
dipasang read-only ke container dan jangan pernah di-commit.

Preflight bawaan mengikat port ke `127.0.0.1:8080` dan
`GATEWAY_BACKGROUND_WORKERS_ENABLED=false`. API dapat diuji lokal, tetapi
Kafka/anchoring/pipeline otomatis belum memproses log. Ini juga bukan mode
read-only penuh: endpoint API yang dipanggil secara eksplisit masih dapat
melakukan operasi bisnis. `/readyz` hanya membuktikan kesiapan koneksi/schema
PostgreSQL, bukan end-to-end Fabric atau Kafka.

Validasi dan mulai hanya service Gateway runtime:

```bash
docker-compose config
docker-compose up -d --build
docker-compose ps
docker-compose logs --tail=100 gateway-runtime
curl -fsS http://127.0.0.1:8080/readyz
```

Jangan jalankan `docker-compose down -v`; service ini tidak memiliki volume
database sendiri dan PostgreSQL/Fabric tetap berada di Besu. Selama preflight,
jangan aktifkan background workers. Untuk cutover CDC, pastikan consumer Gateway
di Besu sudah dihentikan, periksa offset consumer group yang sama, lalu baru
aktifkan `GATEWAY_BACKGROUND_WORKERS_ENABLED=true` dan pilih
`KAFKA_CONSUMER_START_OFFSET=earliest` atau `latest` secara sadar. `earliest`
atau `latest` hanya berlaku jika group belum memiliki offset tersimpan. Jangan
jalankan dua deployment Gateway sebagai consumer produksi bersamaan.

Port hanya dipublikasikan ke localhost secara default. Akses dari dashboard
jarak jauh memerlukan keputusan jaringan/firewall tersendiri; jangan mengubah
bind address menjadi `0.0.0.0` tanpa membatasi akses ke jaringan privat.

Background worker otomatis fail-closed: default
`GATEWAY_BACKGROUND_WORKERS_ENABLED=false` menjaga Kafka consumer, hashing,
aggregator, Fabric anchoring, snapshot worker, verification worker/scheduler,
dan tamper scanner agar tidak berjalan saat service baru dinyalakan. API HTTP
tetap tersedia dan endpoint yang dipanggil eksplisit tetap dapat melakukan
operasi bisnis; flag ini bukan mode read-only untuk seluruh aplikasi.

Sebelum mengaktifkan worker, tetapkan
`GATEWAY_BACKGROUND_WORKERS_ENABLED=true` dan pilih
`KAFKA_CONSUMER_START_OFFSET=earliest` atau `latest`. Nilai itu hanya menjadi
fallback ketika Kafka belum menyimpan offset untuk group/partition tersebut;
offset tersimpan pada group `auditchain-gateway-<client_id>` tetap menjadi
checkpoint yang dipakai. `latest` dapat melewati pesan lama jika group belum
memiliki checkpoint. Saat cutover ke mini PC, hentikan consumer Gateway lama
terlebih dahulu dan jangan menjalankan dua deployment consumer untuk tenant
yang sama secara bersamaan.

Saat background workers diaktifkan, worker verification memproses job yang
dibuat Gateway Dashboard atau scheduler melalui
`POST /api/dashboard/verification-runs`, dalam batch default `100` dari
`VERIFICATION_JOB_BATCH_SIZE`. Nilai tersebut adalah ukuran batch, bukan batas
jumlah log dalam satu range. Client Portal hanya membaca progress dan hasil
terakhir melalui `GET /api/dashboard/verification-runs/latest`.
Worker job ini juga hanya memproses antrean otomatis saat
`GATEWAY_BACKGROUND_WORKERS_ENABLED=true`.

Scheduler backend hanya berjalan jika
`GATEWAY_BACKGROUND_WORKERS_ENABLED=true` dan
`VERIFICATION_SCHEDULER_ENABLED=true`. Ketika keduanya aktif, scheduler
mengambil client berstatus `active`, melanjutkan dari
range completed terakhir dengan overlap kecil, lalu membuat satu
`verification_run` durable per client. Scheduler tetap berjalan tanpa browser
dashboard terbuka. `VERIFICATION_SCHEDULER_RUN_ON_START` hanya dipakai untuk
memulai satu siklus saat Gateway boot; default-nya `false`.
Untuk smoke test satu tenant, isi `VERIFICATION_SCHEDULER_CLIENT_ID` dengan
UUID client tersebut. Jika kosong, semua client berstatus `active` akan diproses.

Tamper Scanner adalah jalur otomatis terpisah dari scheduler dan Verify Range.
Scanner hanya dimulai jika `GATEWAY_BACKGROUND_WORKERS_ENABLED=true` dan
`TAMPER_SCANNER_ENABLED=true`; interval default-nya 300 detik dan batch
default-nya 100 log. Scanner memeriksa hash lokal, Merkle proof, dan anchor
Fabric, lalu menyimpan hasil ke `audit_logs` tanpa tombol dashboard. Status
scheduler dan scanner di server tetap harus dikonfirmasi dari environment serta
log startup runtime.

Consumer tidak meng-commit offset bila pemrosesan atau transaksi DB gagal;
reader dihentikan dan mencoba ulang dari pesan yang sama. Karena tidak ada DLQ
otomatis, payload poison harus diperiksa/diperbaiki sebelum consumer dapat maju
pada partition tersebut. Log consumer hanya memuat identitas topic/partition/
offset dan error ringkas, bukan isi payload CDC.
Ringkasan pemeriksaan terakhir tersedia di `data.integrity_check` pada
`GET /api/dashboard/stats`; data row per log tetap berada di endpoint Gateway.

### Menjalankan via Docker

```bash
docker-compose up -d --build      # Build + jalankan gateway dan PostgreSQL
docker-compose logs -f api-gateway
```

Compose utama memakai jalur direct client-DB dan tidak membuat container atau
binding port MinIO. Jalur ini tetap membutuhkan Fabric, konfigurasi Agent aktif,
recovery token terpisah, dan endpoint `POST /recover/:table/:record_id` pada
Agent client ketika recovery diaktifkan.
Aktifkan recovery hanya setelah migration additive dan smoke test staging
berhasil.
Dalam mode `snapshot_legacy`, `SNAPSHOT_REQUIRED_FOR_ANCHOR=true` membuat
`SNAPSHOT_WRITER_ENABLED` dan konfigurasi MinIO wajib aktif; Gateway akan
menolak start bila tidak. Pada mode `agent_direct`, flag snapshot lama
diabaikan agar environment lama tidak menghidupkan dependency MinIO.

Mode `agent_direct` memulihkan row database operasional client
yang sudah ada melalui Agent, tanpa SQL arbitrer dan tanpa membuat tabel baru.
Request client tidak membutuhkan approval admin; tenant tetap dibatasi oleh
`client_id` JWT.

Mode `snapshot_legacy` masih tersedia hanya untuk kompatibilitas/rollback. Jika
mode itu memang diperlukan, jalankan Compose dengan override khusus berikut:

```bash
docker-compose -f docker-compose.yml -f docker-compose.snapshot.yml up -d --build api-gateway
```

Override tersebut menggunakan port host `19000` dan `19001` untuk menghindari
konflik dengan service MinIO lain. Jangan mengaktifkan snapshot legacy hanya
karena variabel MinIO masih tersimpan di environment lama.

### Deployment development melalui GitHub Actions

Merge ke `main` tidak langsung merestart server development. Setelah merge, buka
**Actions** dan jalankan workflow **Deploy Backend Development** secara manual
dari branch `main`. Workflow menjalankan test/build, mengirim satu secret
multiline `BACKEND_ENV` secara sementara, menjalankan `deploy.sh` melalui SSH,
membangun ulang `api-gateway`, lalu menunggu health/readiness. Dengan demikian
tidak perlu lagi menjalankan `git pull` dan `docker compose up --build -d` secara
manual di server.

Detail secret, prasyarat server, verifikasi, dan troubleshooting tersedia pada
[Development Deployment Runbook](docs/DEVELOPMENT_DEPLOYMENT_RUNBOOK.md) serta
[Implementation Plan GitHub Actions](docs/GITHUB_ACTIONS_MANUAL_DEPLOYMENT_PLAN.md).

---

## 📡 API Endpoints Utama

| Method | Endpoint | Auth | Deskripsi |
| :--- | :--- | :--- | :--- |
| `POST` | `/api/auth/register` | ❌ | Registrasi user baru ke suatu client |
| `POST` | `/api/auth/login` | ❌ | Login → JWT token |
| `POST` | `/api/admin/clients` | 🔑 Admin Secret | Registrasi tenant baru + generate API Key |
| `POST` | `/api/admin/kafka-config` | 🔑 Admin Secret | Registrasi konfigurasi Kafka per klien |
| `PATCH` | `/api/admin/kafka-config/:id/toggle` | 🔑 Admin Secret | Aktif/nonaktifkan stream Kafka (tanpa hapus data) |
| `DELETE` | `/api/admin/kafka-config/:id` | 🔑 Admin Secret | Soft-delete konfigurasi Kafka |
| `GET` | `/api/dashboard/stats` | 🔐 JWT | Statistik total/anchored/pending |
| `GET` | `/api/dashboard/logs` | 🔐 JWT | Log terbaru (pagination: `page`, `page_size`, filter `integrity_status`) |
| `GET` | `/api/dashboard/logs/by-resource/:resource` | 🔐 JWT | Riwayat log per resource |
| `GET` | `/api/dashboard/verify/:log_id` | 🔐 JWT | Verifikasi 4-Layer untuk satu log (on-demand) |
| `GET` | `/api/dashboard/verify-resource/:resource` | 🔐 JWT | Verifikasi seluruh riwayat satu resource |
| `GET` | `/api/dashboard/verify-range/internal?from=&to=` | 🔐 JWT | Verifikasi seluruh log dalam rentang waktu untuk gateway dashboard (sinkron, maksimal 100 log) |
| `GET` | `/api/dashboard/verify-range?from=&to=` | 🔐 JWT | Alias kompatibilitas untuk route internal; client baru sebaiknya memakai `/internal` |
| `GET` | `/api/dashboard/verify-range/client?from=&to=` | 🔐 JWT | Verifikasi resource terbaru untuk client portal dan memperbarui statistik client |
| `GET` | `/api/dashboard/fabric/:anchor_id` | 🔐 JWT | Ambil data raw dari Fabric World State |
| `POST` | `/api/dashboard/verify-data` | 🔐 JWT | Verifikasi integritas data aktual vs audit trail |
| `GET` | `/api/dashboard/inventory` | 🔐 JWT | Daftar resource unik yang termonitor |
| `GET` | `/api/dashboard/recovery/incidents` | 🔐 JWT | Daftar tamper incident |
| `GET` | `/api/dashboard/recovery/incidents/:id` | 🔐 JWT | Detail tamper incident |
| `GET` | `/api/dashboard/recovery/resources/:resource/versions` | 🔐 JWT | Daftar event client ter-anchor untuk referensi recovery |
| `GET` | `/api/dashboard/recovery/requests` | 🔐 JWT | Daftar recovery request, opsional filter `status` |
| `GET` | `/api/dashboard/recovery/requests/:id` | 🔐 JWT | Detail recovery request |
| `POST` | `/api/dashboard/recovery/requests` | 🔐 JWT | Membuat permintaan recovery |
| `POST` | `/api/dashboard/recovery/requests/:id/execute` | 🔐 Client JWT | Menjalankan recovery terverifikasi |
| `POST` | `/api/dashboard/agent/config` | 🔐 JWT | Registrasi/update konfigurasi Universal Agent klien |
| `GET` | `/api/dashboard/agent/ping` | 🔐 JWT | Cek konektivitas Agent klien |

> Catatan: dua endpoint `logs/by-resource` dan `verify-resource` direncanakan digabung menjadi satu endpoint, namun masih tertunda menunggu penyelesaian debugging konfigurasi Agent.

Kontrak recovery direct, state machine, payload Agent, dan failure behavior
dirangkum pada [Recovery API Contract](docs/RECOVERY_API_CONTRACT.md).

### Kontrak status riwayat resource

Pada response `GET /api/dashboard/verify-resource/:resource`, setiap item log
memiliki tiga dimensi status yang independen:

- `integrity_status` / `chain_status`: validasi PostgreSQL Gateway, Merkle
  proof, dan anchor Fabric (`valid`, `tampered`, `pending`, `unreachable`).
- `integrity_source` dan `integrity_run_id`: sumber pemeriksaan terakhir per
  log (`TAMPER_SCANNER`, `MANUAL_VERIFY_RANGE`, background/scheduled run,
  single-log check, atau recovery). Data lama sebelum provenance tersedia
  memiliki source kosong dan tidak di-backfill dengan tebakan.
- `agent_status`: pemeriksaan live ke Agent client untuk log terbaru
  (`matched`, `mismatch`, `unreachable`, `not_configured`); status ini tidak
  menurunkan status Gateway/Fabric.
- `recovery_status`: provenance recovery (`not_recovered`, `pending`,
  `recovered`, `failed`). Saat request recovery berhasil, item target tetap
  `integrity_status=valid` dan dapat diberi badge `recovered` meskipun Agent
  sedang tidak dapat dihubungi.

---

## 🕵️ Verifikasi 4-Layer — Detail

```
Layer 1 (DB Existence)     → Cek log ada di PostgreSQL
Layer 2 (Re-Hash)          → Hitung ulang SHA3-256, bandingkan dengan hash tersimpan
Layer 3 (Agent Source)     → Panggil Universal Agent klien, bandingkan field aktual
                              (HANYA dijalankan untuk log TERBARU per resource)
Layer 4 (Merkle/Blockchain)→ Rekonstruksi Merkle Root dari proof chain,
                              bandingkan dengan root di ledger Hyperledger Fabric
```

Jika terjadi ketidakcocokan, status `tampered` disertai `chain_issues`:
- `merkle_mismatch` — rekonstruksi Merkle root tidak cocok dengan Fabric ledger (istilah ini dipilih alih-alih "blockchain tampered" karena gateway tidak bisa membuktikan secara kriptografis apakah manipulasi terjadi di ledger Fabric atau di tabel proof lokal).
- `client_mismatch:<log_id>` — data live di klien (via Agent) sudah berbeda dari log terbaru resource tersebut.

---

## 🛡️ Threat Model & Keamanan

Sistem ini kebal terhadap berbagai jenis serangan pada level database:

- **Modifikasi data di DB Gateway (Layer 2):** Jika data lokal diubah, mesin Re-Hashing akan mendeteksi ketidaksesuaian hash.
- **Modifikasi data + Re-Hash (Layer 4):** Jika hash ditimpa juga, verifikasi Merkle Tree terhadap Fabric ledger akan rusak.
- **Modifikasi data di sisi klien (Layer 3):** Jika data di database klien berubah tanpa melalui AuditChain, Agent akan melaporkan `client_mismatch` saat dibandingkan dengan log terbaru.
- **Modifikasi/penghapusan seluruh DB Gateway:** Merkle Root yang sudah di-anchor di Hyperledger Fabric tidak bisa dipalsukan ulang tanpa terdeteksi saat rekonstruksi proof gagal cocok dengan ledger.

### ⚠️ Isu Keamanan Pra-Produksi (Diketahui, Didefer hingga sebelum go-live `auditchain.id`)

- CORS saat ini `AllowAllOrigins = true` — perlu di-whitelist sebelum produksi
- Validasi `JWT_SECRET` kosong belum diterapkan saat startup
- Beberapa endpoint admin lama berpotensi tanpa autentikasi penuh — sedang direview
- Kredensial hardcoded di beberapa `docker-compose.yml` (mis. `POSTGRES_PASSWORD`) — akan dipindah ke secret manager
- Rencana deployment: `auditchain.id` (dashboard) + `api.auditchain.id` (gateway API) via Cloudflare Tunnel + Caddy reverse proxy

---

## 🧹 Dead Code / Item Teknis Tertunda

- `internal/modules/ingestion` — route belum terdaftar di router utama (lihat bagian Arsitektur)
- `kafkaconsumer/verifier.go` — tidak lagi digunakan, sudah ditandai untuk dihapus
- Cron job hybrid untuk verifikasi otomatis log yang baru ter-anchor — didesain namun menunggu persetujuan manager sebelum diaktifkan
- Frontend dashboard (`src/App.js`) perlu disesuaikan untuk membaca `res.data.data` sesuai response shape baru `GetRecentLogs` (`{"data": [...], "pagination": {...}, "note"?: "..."}`)

---

## 🧩 Ekosistem Terkait

| Repository | Fungsi |
|---|---|
| `auditchain-gateway-backend` (repo ini) | API Gateway, verifikasi, blockchain anchoring |
| `auditchain-agent` | Agent yang dipasang di sisi klien untuk verifikasi Layer 3 (mendukung Oracle & PostgreSQL) |
| Dashboard Frontend (React) | UI Auditor & Admin Panel |
| Landing Page (React + Vite) | Halaman promosi/pengenalan produk |

---

## 📄 Lisensi & Kontak

Internal project — hubungi tim pengembang untuk detail lisensi dan akses lebih lanjut.
