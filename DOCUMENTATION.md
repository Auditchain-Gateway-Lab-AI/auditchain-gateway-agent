# Dokumentasi Migrasi & API Agent (Oracle Database & SIMRS Morbis)

Dokumentasi ini menjelaskan pembaruan arsitektur pada **AuditChain Gateway Agent** dari PostgreSQL ke **Oracle Database** untuk kebutuhan integrasi verifikasi data pada sistem **SIMRS Morbis** dan **Satu Peta**.

---

## 1. Ringkasan Migrasi Database (PostgreSQL ke Oracle)

Agent mempertahankan endpoint verifikasi read-only dan sekarang memiliki tambahan direct recovery write yang feature-flagged. Recovery tetap **disabled by default**. Koneksi database telah sepenuhnya dimigrasikan dari PostgreSQL ke Oracle Database dengan rincian sebagai berikut:

### Teknologi & Driver
* **Driver**: Menggunakan `github.com/sijms/go-ora/v2` yang merupakan driver murni (*pure Go*) Oracle. Tidak membutuhkan instalasi Oracle Instant Client (OCI) atau CGO, sehingga sangat ringan dan portabel.
* **Koneksi via SID**: Alamat koneksi di-build secara otomatis menggunakan opsi **SID** (Service Identifier) Oracle (dalam kasus ini: `orclcdb`), bukan Service Name, untuk memastikan kecocokan dengan konfigurasi listener database SIMRS Morbis.

### Optimasi Query Dialek Oracle
1. **Resolusi Schema Owner Dinamis**:
   Pengecekan eksistensi tabel dilakukan dengan mencari pada tabel sistem `all_tables`. Pencarian memprioritaskan skema user aktif saat ini (`USER`), dan otomatis melakukan fallback ke skema lain jika tabel (seperti `BENTUK_MAKANAN`) dimiliki oleh skema yang berbeda (misalnya skema `SIMRS`).
2. **Deteksi Primary Key Constraint Otomatis**:
   Pencarian primary key tidak lagi bergantung pada daftar kandidat statis (`id`, `fid`, dll.). Agent akan menanyakan katalog constraint Oracle (`all_constraints` dan `all_cons_columns`) untuk mendeteksi kolom PK asli yang didefinisikan secara resmi pada tabel tersebut (contoh: `KODE_BENTUK_MAKANAN`).
3. **Penyaringan Tipe Data (Exclusion)**:
   Penyaringan otomatis kolom spasial (`SDO_GEOMETRY`) dan kolom binary besar (`BLOB`, `CLOB`, `RAW`, `LONG`) guna mempercepat query dan menjaga performa pengiriman data.
4. **Paging Limit**:
   Pembatasan pencarian baris menggunakan dialek khas Oracle (`ROWNUM <= 1`) menggantikan keyword PostgreSQL (`LIMIT 1`).

---

## 2. Struktur API Verifikasi (Inbound)

Semua endpoint verifikasi berjalan pada port verify yang ditentukan (default: `9090`) dan memerlukan autentikasi token Bearer.

### A. Endpoint Verifikasi SIMRS (`/verify/<table>/<id>`)
Endpoint ini digunakan oleh AuditChain Gateway untuk mengambil data non-spasial asli langsung dari database klien (SIMRS Morbis).

* **Method**: `GET`
* **URL**: `http://<agent-ip>:<port>/verify/{nama_tabel}/{id_data}`
* **Headers**:
  ```http
  Authorization: Bearer <AGENT_VERIFY_TOKEN>
  ```
* **Contoh Request**:
  `GET http://localhost:9090/verify/BENTUK_MAKANAN/1`

* **Contoh Response Payload (Data Ditemukan - `200 OK`)**:
  ```json
  {
    "found": true,
    "table": "BENTUK_MAKANAN",
    "id": "1",
    "data": {
      "KODE_BENTUK_MAKANAN": "1",
      "NAMA_BENTUK": "Cair",
      "KETERANGAN": "Bentuk makanan berupa cairan/bubur halus"
    },
    "checked_at": "2026-07-10T10:05:00.123456Z"
  }
  ```

* **Contoh Response Payload (Data Tidak Ditemukan - `200 OK`)**:
  ```json
  {
    "found": false,
    "table": "BENTUK_MAKANAN",
    "id": "999",
    "data": null,
    "checked_at": "2026-07-10T10:05:30.987654Z"
  }
  ```

---

### B. Endpoint Verifikasi Satu Peta Geospatial (`/verify-resource/<table>/<id>`)
Endpoint ini digunakan untuk memverifikasi data spasial (Satu Peta) dengan cara kerja yang serupa dengan endpoint SIMRS.

* **Method**: `GET`
* **URL**: `http://<agent-ip>:<port>/verify-resource/{nama_tabel}/{id_data}`
* **Headers**:
  ```http
  Authorization: Bearer <AGENT_VERIFY_TOKEN>
  ```

---

### C. Endpoint Health Check (`/health`)
Digunakan untuk memonitor status keaktifan server verify agent.

* **Method**: `GET`
* **URL**: `http://<agent-ip>:<port>/health`
* **Response Payload (`200 OK`)**:
  ```json
  {
    "status": "ok"
  }
  ```

`/readyz` juga tersedia untuk readiness dan mengembalikan `503
database_unreachable` bila koneksi Oracle tidak siap. `/health` hanya liveness.

### D. Endpoint Direct Recovery (`/recover/<table>/<record_id>`)

Endpoint ini hanya aktif bila `AGENT_RECOVERY_ENABLED=true`, token recovery
terpisah telah dipasang, dan policy lokal telah direview. Gateway mengirim
state yang sudah divalidasi; Agent tidak menerima SQL, schema, primary-key
column, atau predicate dari request.

* **Method**: `POST`
* **Headers**:
  ```http
  Authorization: Bearer <AGENT_RECOVERY_TOKEN>
  Content-Type: application/json
  Accept: application/json
  ```
* **Body**: mengikuti contract pada
  `docs/AUDITCHAIN_AGENT_DIRECT_RECOVERY_IMPLEMENTATION_PLAN.md`, dengan
  operasi `UPSERT`, `DELETE`, atau defensive `NOOP`.
* **Response** hanya berisi request ID, hash sebelum/sesudah, status readback,
  dan `found_after`; raw row, password, token, dan desired state tidak pernah
  dikembalikan.

Response `2xx` hanya berarti Agent berhasil melakukan transaction dan
readback. Gateway tetap menunggu CDC, Merkle, dan Fabric confirmation sebelum
menandai recovery final `SUCCEEDED`.

### E. Endpoint `/table`

`/table/<table>` dinonaktifkan secara default karena dapat mengekspos banyak
baris. Jangan mengaktifkannya di production; bila benar-benar diperlukan untuk
diagnostic sementara, gunakan `AGENT_ENABLE_TABLE_ENDPOINT=true` melalui
deployment yang terkontrol.

---

## 3. Konfigurasi Environment (`.env`)

Sesuaikan variabel lingkungan pada berkas `.env` di root direktori Agent dengan kredensial Oracle SIMRS Morbis Anda:

```env
DB_HOST=192.0.2.10          # placeholder IP Database Server
DB_PORT=1521                # Port Listener Oracle
DB_USER=client_user         # placeholder username
DB_PASSWORD=change-me       # do not commit a real password
DB_NAME=ORCLCDB             # SID Oracle Database

# Token Autentikasi Keamanan (Harus sama dengan config di Gateway)
AGENT_VERIFY_TOKEN=replace-with-a-long-read-token
AGENT_VERIFY_PORT=9090

# Recovery aman secara default: tetap false sampai pilot disetujui.
AGENT_RECOVERY_ENABLED=false
AGENT_CLIENT_ID=
AGENT_RECOVERY_TOKEN=
AGENT_RECOVERY_STATE_PATH=/var/lib/auditchain-agent/recovery-state.db
AGENT_RECOVERY_MAX_BODY_BYTES=1048576
AGENT_RECOVERY_MAX_COLUMNS=64
AGENT_RECOVERY_MAX_CLOCK_SKEW_SECONDS=300
AGENT_RECOVERY_REQUEST_TIMEOUT_SECONDS=15
AGENT_RECOVERY_RETENTION_DAYS=90
AGENT_RECOVERY_POLICY_PATH=/app/recovery-policy.yml
```

`AGENT_RECOVERY_TOKEN` minimal 32 byte acak dan wajib berbeda dari
`AGENT_VERIFY_TOKEN`. Policy dibaca dari file read-only dan menggunakan exact
schema/table/column mapping. Mulai dari satu tabel non-sensitif; jangan
mengaktifkan tabel credential atau password.

## 4. State Idempotency dan Rollback

State command recovery disimpan di Agent-local durable store pada
`AGENT_RECOVERY_STATE_PATH`, bukan tabel baru di Oracle client. Volume tersebut
harus writable hanya oleh user Agent dan dipertahankan saat container restart.

Untuk rollback, set `AGENT_RECOVERY_ENABLED=false` lalu restart Agent. Jangan
menghapus state store dan jangan membalikkan row secara otomatis; command yang
`IN_PROGRESS` atau `OUTCOME_UNKNOWN` harus direkonsiliasi dengan Gateway.

## 5. Verifikasi Local

```bash
gofmt -l .
go mod verify
go vet -p 1 ./...
go test -p 1 ./... -count=1 -timeout=120s
go test -race ./...
go build -p 1 ./...
git diff --check
docker compose config
```

Oracle transaction integration, CDC, Merkle/Fabric confirmation, staging
failure drill, dan rollback evidence harus dijalankan pada environment staging
yang disetujui; local unit/build pass tidak menggantikan bukti tersebut.

---

## 6. Cara Menjalankan

Lakukan instalasi ulang dependency dan jalankan aplikasi Agent:

```bash
# 1. Install & tidy dependency Go
go mod tidy

# 2. Jalankan Server Agent
go run main.go
```
