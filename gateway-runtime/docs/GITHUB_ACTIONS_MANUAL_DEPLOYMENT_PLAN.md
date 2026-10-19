# Implementation Plan GitHub Actions Manual Deployment

## 1. Informasi Dokumen

| Item | Nilai |
|---|---|
| Proyek | AuditChain Gateway Backend |
| Tanggal | 22 September 2026 |
| Status | Implementasi repository selesai; menunggu konfigurasi GitHub/server dan staging drill |
| Branch saat analisis | `nama-fitur-baru` |
| Commit baseline | `cedf21a36c49b2fdbfe9295005990112e18f01b1` |
| Commit `origin/main` saat analisis | `cedf21a36c49b2fdbfe9295005990112e18f01b1` |
| Target | Server Besu yang sekarang menjalankan Docker Compose |
| Pemicu deployment | Tombol **Run workflow** di GitHub Actions |

Dokumen ini menggantikan rancangan artifact/GHCR sebelumnya. Kebutuhan yang
sebenarnya adalah menghilangkan pekerjaan manual operator di server. Perintah
`git pull` dan Docker Compose masih boleh digunakan, tetapi harus dijalankan
secara otomatis oleh GitHub Actions.

> **Perubahan scope saat implementasi:** server Besu yang tersedia adalah
> **development**, bukan production. Karena itu workflow aktif menggunakan mode
> `development`, environment `development`, dan tombol **Deploy Backend
> Development**. Referensi production di bagian-bagian berikut adalah desain
> hardening/cutover lanjutan; gunakan
> [Development Deployment Runbook](DEVELOPMENT_DEPLOYMENT_RUNBOOK.md) untuk
> deployment sekarang.

---

## 2. Tujuan yang Disepakati

Kondisi sekarang:

```text
merge feature ke main
    -> operator login/SSH ke server Besu
    -> cd ke repository
    -> git pull
    -> docker compose up --build -d api-gateway
    -> operator mengecek container secara manual
```

Kondisi target:

```text
feature branch
    -> Pull Request
    -> merge ke main
    -> tidak deploy otomatis
    -> operator membuka GitHub Actions
    -> klik Run workflow
    -> GitHub Actions menjalankan test
    -> GitHub Actions masuk ke server Besu melalui SSH
    -> server otomatis update main
    -> server otomatis build dan restart api-gateway
    -> GitHub Actions menunggu health check
    -> sukses atau rollback
```

Setelah implementasi, operator tidak perlu lagi login ke server untuk deployment
normal. Seluruh aktivitas dan hasil deployment terlihat pada log GitHub Actions.

---

## 3. Keputusan Desain

1. Workflow production hanya memakai `workflow_dispatch`.
2. Merge atau push ke `main` tidak langsung deploy.
3. Tombol **Run workflow** hanya boleh men-deploy commit dari `main`.
4. GitHub-hosted runner menjalankan validasi kode dan menghubungi server.
5. GitHub Actions menggunakan SSH key, bukan password.
6. Jika server hanya dapat dijangkau melalui Tailscale, GitHub Actions bergabung
   ke tailnet sebelum SSH.
7. Server tetap mempunyai clone repository; isi `.env` production dikelola
   sebagai satu multiline GitHub Environment secret `BACKEND_ENV`, lalu
   disinkronkan secara aman ke shared `.env` server setiap deployment.
8. GitHub Actions menjalankan `deploy.sh` di server dengan branch `main` dan
   expected commit SHA.
9. Deployment rutin hanya membangun dan mengganti `api-gateway`; PostgreSQL dan
   MinIO tidak direcreate.
10. Deployment baru dinyatakan berhasil setelah health check dan smoke test.
11. Hanya satu deployment production yang boleh berjalan dalam satu waktu.
12. Commit dan image sebelumnya dicatat untuk rollback.

Rancangan ini sengaja tidak menambah GHCR, registry image, Kubernetes, atau
release artifact besar karena hal-hal tersebut bukan kebutuhan utama saat ini.

---

## 4. Kondisi Repository Saat Ini

### 4.1 Yang sudah dapat digunakan

- `.github/workflows/deploy-dev.yml` sudah membuktikan bahwa deployment dapat
  dipicu melalui GitHub Actions.
- `deploy.sh` sudah dapat memilih branch melalui `DEPLOY_BRANCH`.
- `deploy.sh` sudah memakai `git pull --ff-only`.
- `docker-compose.yml` sudah mempunyai service `api-gateway`.
- PostgreSQL dan MinIO memakai named volume.
- Material Fabric dipasang read-only dari host.
- Aplikasi Go menangani `SIGTERM` dan graceful shutdown.
- Test Go tersedia pada modul-modul utama.

### 4.2 Yang belum sesuai kebutuhan

- Workflow masih otomatis dipicu oleh push ke `ops-Petrus`.
- Workflow masih hardcoded menjalankan branch `ops-Petrus`.
- Remote branch `ops-Petrus` sudah tidak tersedia.
- Workflow lama menggunakan self-hosted runner secara langsung, sedangkan pola
  target menggunakan GitHub-hosted runner dan SSH seperti RekaKarbon.
- `deploy.sh` menjalankan `docker compose up -d --build` untuk seluruh stack,
  bukan khusus `api-gateway`.
- Belum ada expected SHA check; `main` dapat berubah antara klik workflow dan
  proses `git pull`.
- `api-gateway` belum mempunyai health check.
- Belum ada endpoint `/healthz` dan `/readyz`.
- Belum ada deployment lock.
- Belum ada rollback otomatis.
- `docker compose ps` hanya menunjukkan status container, bukan kesiapan API.
- Key pada `.env` aktif dan `.env.example` belum sepenuhnya sinkron. Kontrak env
  canonical harus diselesaikan sebelum isi production dipindahkan ke GitHub.

### 4.3 Keputusan setelah pemeriksaan: apakah perlu artifact dan SCP?

Untuk kondisi repository dan server saat ini, **artifact aplikasi melalui SCP
belum diperlukan**.

Alasannya:

1. Unit deployment AuditChain adalah service Docker Compose, bukan aplikasi
   bare-metal seperti backend RekaKarbon.
2. Server sudah mempunyai clone repository dan `deploy.sh` yang memang dirancang
   untuk fetch/pull lalu membangun image lokal.
3. Source repository yang ter-track relatif kecil untuk Git, sementara sekitar
   6,8 MB dari 7,5 MB file ter-track adalah dokumentasi/gambar yang tidak menjadi
   runtime utama.
4. Runtime membutuhkan state yang tidak boleh dikirim sebagai artifact:
   - `.env` production di server;
   - named volume PostgreSQL;
   - named volume MinIO;
   - external network `fabric_test`;
   - material organisasi Fabric dari absolute host path.
5. Compose memakai `./scripts/minio` dari checkout server untuk initializer.
6. Mengirim source archive melalui SCP lalu membangun lagi di server hanya
   mengganti mekanisme transfer Git dengan SCP, tetapi tidak menghilangkan build
   di server atau meningkatkan konsistensi runtime secara berarti.
7. Mengirim Docker image sebagai tar melalui SCP dapat dilakukan, tetapi ukuran
   image biasanya jauh lebih besar daripada perubahan Git dan setiap deploy
   harus mentransfer archive image kembali.

Karena itu rancangan tahap pertama tetap:

```text
GitHub Actions
    -> SSH ke server
    -> git fetch/pull exact main SHA
    -> docker compose build api-gateway
    -> docker compose up api-gateway
    -> health check
```

SCP tidak diperlukan untuk payload aplikasi pada tahap pertama. SSH tetap
diperlukan untuk menjalankan deployment di server.

### 4.4 Kapan deployment artifact menjadi perlu?

Evaluasi ulang OCI image artifact bila salah satu kondisi berikut muncul:

- server production tidak boleh menyimpan source atau Git credential;
- build di server terlalu lambat atau mengganggu resource production;
- satu release harus didistribusikan ke lebih dari satu server;
- dibutuhkan bukti bahwa image yang diuji sama persis dengan image yang jalan;
- rollback harus berbasis immutable image digest;
- tim membutuhkan SBOM, provenance, atau vulnerability gate untuk image;
- server baru harus dapat dipulihkan tanpa repository checkout.

Jika kondisi tersebut muncul, urutan pilihan yang direkomendasikan:

1. build OCI image di GitHub Actions dan push ke GHCR;
2. server pull exact image digest lalu menjalankan Compose;
3. gunakan `docker save` + SCP + `docker load` hanya bila registry tidak dapat
   digunakan tetapi immutable image tetap diwajibkan.

Source archive SCP seperti RekaKarbon bukan pilihan utama untuk AuditChain
karena target runtime-nya adalah container.

### 4.5 Temuan build context

Repository belum mempunyai `.dockerignore`, sedangkan Dockerfile menjalankan
`COPY . .` pada builder stage. File `.env` memang tidak disalin secara eksplisit
ke final runtime stage, tetapi tanpa `.dockerignore` file tersebut tetap dapat
masuk build context dan builder layer/cache. Sebelum workflow production
menjalankan remote build, wajib ditambahkan `.dockerignore` yang minimal
mengecualikan:

- `.git`;
- `.env` dan variasi environment rahasia;
- `wallet` dan `postman` lokal;
- binary/archive build lokal;
- gambar dokumentasi yang tidak diperlukan build.

Folder `scripts` dan file Go pada `docs/docs.go` tidak boleh dikecualikan karena
dipakai runtime/build saat ini.

---

## 5. File yang Direncanakan Berubah

| File | Tindakan | Tujuan |
|---|---|---|
| `.github/workflows/deploy-production.yml` | Tambah | Workflow manual production seperti RekaKarbon |
| `.github/workflows/deploy-dev.yml` | Pertahankan selama transisi | Mencegah perubahan jalur lama sebelum workflow baru terbukti |
| `deploy.sh` | Perkuat | Main-only input, expected SHA, lock, API-only deploy, health, rollback |
| `internal/api/health.go`, `internal/api/health_test.go`, `main.go` | Tambah/ubah | Implementasi serta registrasi test health/readiness |
| `docker-compose.yml` | Ubah minimal | Tambahkan health check `api-gateway` bila contract sudah tersedia |
| `.dockerignore` | Tambah | Cegah `.env`, `.git`, dan file lokal masuk build context/cache |
| `.env.example` | Ubah bila diperlukan | Dokumentasikan konfigurasi health tanpa secret |
| `README.md` | Ubah | Dokumentasikan tombol deployment dan link runbook |
| `docs/PRODUCTION_DEPLOYMENT_RUNBOOK.md` | Tambah | Panduan deploy, verifikasi, error, dan rollback |

Tidak ada `.env`, SSH private key, password, atau token production yang boleh
masuk repository.

---

## 6. Workflow GitHub Actions Target

### 6.1 Trigger

Workflow hanya menggunakan trigger manual:

```yaml
on:
  workflow_dispatch:
    inputs:
      confirm_production:
        type: boolean
        required: true
        default: false
      deployment_reason:
        type: string
        required: true
```

Tidak ada trigger `push` pada workflow production.

### 6.2 Pengamanan branch

Pengamanan berlapis:

1. operator harus memilih branch `main` pada dropdown **Run workflow**;
2. job mempunyai condition `github.ref == 'refs/heads/main'`;
3. GitHub Environment `production` hanya mengizinkan branch `main`;
4. `deploy.sh` menerima `EXPECTED_SHA=${{ github.sha }}`;
5. script memeriksa `origin/main` sama dengan expected SHA sebelum restart.

Jika `main` mendapat commit baru setelah workflow dimulai, workflow berhenti dan
operator menjalankan ulang workflow pada SHA terbaru. Script tidak boleh diam-
diam men-deploy commit yang berbeda dari tampilan GitHub Actions.

### 6.3 Job `validate`

Berjalan pada `ubuntu-latest` sebelum server disentuh:

1. checkout exact workflow SHA;
2. setup Go sesuai `go.mod`;
3. cek formatting Go;
4. jalankan `go vet -p 1 ./...`;
5. jalankan `go test -p 1 ./... -count=1 -timeout=120s`;
6. jalankan `go build -p 1 ./...`;
7. validasi syntax shell deployment;
8. validasi Docker Compose bila Docker tersedia;
9. hentikan workflow jika salah satu pemeriksaan gagal.

Tidak disediakan tombol `skip_tests` untuk production pada implementasi awal.

### 6.4 Job `deploy_production`

Berjalan setelah `validate` berhasil:

- `runs-on: ubuntu-latest`;
- memakai `environment: production`;
- menunggu approval jika required reviewer diaktifkan;
- memakai concurrency group `backend-production-deploy`;
- memakai `cancel-in-progress: false`;
- mempunyai timeout eksplisit;
- bergabung ke Tailscale bila host hanya tersedia di tailnet;
- memverifikasi SSH host fingerprint;
- terhubung ke server menggunakan SSH key;
- menjalankan `deploy.sh` dengan branch `main` dan expected SHA;
- meneruskan status, health check, dan log aman dari `deploy.sh`;
- melakukan cleanup koneksi dan credential dengan `if: always()`.

Perintah konseptual remote:

```sh
cd /home/besu/auditchain/middleware-AuditChain-Gateway
DEPLOY_BRANCH=main \
EXPECTED_SHA=<github-sha> \
DEPLOY_SERVICE=api-gateway \
bash deploy.sh
```

Path server di atas berasal dari workflow yang sekarang dan wajib diverifikasi
pada Fase 0 sebelum implementasi.

### 6.5 GitHub Actions secrets

Secrets environment `production` yang direncanakan:

- `BACKEND_ENV`: satu multiline secret berisi seluruh isi `.env` aplikasi
  production;
- `BACKEND_PROJECT_DIR`: absolute path checkout repository pada server Besu;
- `PRODUCTION_SSH_HOST`;
- `PRODUCTION_SSH_PORT`;
- `PRODUCTION_SSH_USERNAME`;
- `PRODUCTION_SSH_PRIVATE_KEY`;
- `PRODUCTION_SSH_HOST_FINGERPRINT`;
- `TAILSCALE_OAUTH_CLIENT_ID` jika diperlukan;
- `TAILSCALE_OAUTH_SECRET` jika diperlukan.

Ketentuan:

- `BACKEND_ENV` adalah source of truth konfigurasi aplikasi production;
- secret SSH dan Tailscale tetap terpisah dari `BACKEND_ENV` karena dipakai oleh
  GitHub Actions sebelum workflow dapat mengakses server;
- gunakan SSH deploy key khusus workflow;
- jangan memakai password SSH jika key dapat digunakan;
- SSH user bukan root;
- `sudo` dibatasi atau tidak digunakan;
- secrets baru tersedia setelah environment approval;
- jangan mencetak isi secret pada log.

### 6.6 Pengelolaan satu `BACKEND_ENV` seperti RekaKarbon

Konsepnya sama dengan workflow RekaKarbon:

```text
GitHub Environment production
    -> secret BACKEND_ENV berisi seluruh file env
    -> workflow membuat temporary env file
    -> file dikirim ke server
    -> server memvalidasi dan memasang shared .env
    -> Docker Compose membaca shared .env
```

AuditChain memakai satu secret untuk seluruh env aplikasi agar perubahan
konfigurasi tidak memerlukan puluhan GitHub secrets terpisah. Contoh struktur
nilai secret, tanpa nilai asli:

```dotenv
PORT=...
APP_ENV=...
DB_DSN=...
JWT_SECRET=...
ADMIN_SECRET=...
FABRIC_MSP_ID=...
FABRIC_PEER_ENDPOINT=...
FABRIC_TLS_CERT_PATH=...
FABRIC_CERT_PATH=...
FABRIC_KEY_PATH=...
FABRIC_CHANNEL=...
FABRIC_CHAINCODE=...
MINIO_ENDPOINT=...
MINIO_BUCKET=...
MINIO_ACCESS_KEY=...
MINIO_SECRET_KEY=...
SNAPSHOT_ENCRYPTION_ACTIVE_KEY_ID=...
SNAPSHOT_ENCRYPTION_KEY=...
RECOVERY_ENABLED=...
SNAPSHOT_WRITER_ENABLED=...
SNAPSHOT_REQUIRED_FOR_ANCHOR=...
TAMPER_SCANNER_ENABLED=...
```

Daftar final tidak boleh disalin hanya dari `.env` lokal atau hanya dari
`.env.example`. Daftar harus direkonsiliasi dari:

1. seluruh `os.Getenv` pada source Go;
2. seluruh `${VAR}` pada Docker Compose;
3. feature flags recovery/snapshot/tamper;
4. konfigurasi Fabric, MinIO, database, Redis, dan Tailscale yang benar-benar
   dipakai production;
5. `.env` server aktif tanpa membaca nilainya ke log.

### 6.7 Cara sinkronisasi env yang aman

Workflow tidak menulis `${{ secrets.BACKEND_ENV }}` langsung ke inline remote
shell. Urutan yang direncanakan:

1. map secret ke environment step GitHub Actions;
2. aktifkan `umask 077`;
3. tulis nilai ke file sementara di `$RUNNER_TEMP` tanpa mencetak isinya;
4. pastikan file tidak kosong dan mempunyai key wajib;
5. kirim file melalui SCP ke nama temporary unik berdasarkan run ID;
6. pada server, set permission temporary file ke `0600`;
7. validasi format, duplicate key, serta key wajib tanpa mencetak value;
8. simpan backup env aktif dengan timestamp;
9. pasang env baru melalui atomic rename;
10. arahkan `.env` repository ke shared env bila memakai symlink;
11. jalankan `docker compose config --quiet` sebelum build/restart;
12. bila validasi/deployment gagal karena konfigurasi, pulihkan env sebelumnya;
13. hapus file temporary pada runner dan server dengan `if: always()`;
14. simpan backup di luar repository tanpa memasukkannya ke artifact atau log
    GitHub; retensi/pembersihan dilakukan sebagai maintenance terjadwal setelah
    jumlah backup yang dipertahankan disepakati.

Struktur server yang direkomendasikan:

```text
/home/besu/auditchain/shared/
|-- backend.env
`-- env-backups/
    `-- backend.env.<timestamp>

/home/besu/auditchain/middleware-AuditChain-Gateway/.env
    -> /home/besu/auditchain/shared/backend.env
```

Permission `backend.env`, backup, dan temporary env harus `0600` serta dimiliki
oleh SSH deployment user.

---

## 7. Desain `deploy.sh` yang Aman

### 7.1 Input

Script menerima:

- `DEPLOY_BRANCH`, production wajib `main`;
- `EXPECTED_SHA`, wajib berupa 40 karakter hexadecimal;
- `DEPLOY_SERVICE`, production wajib `api-gateway`;
- timeout health check dengan default yang aman.

### 7.2 Preflight

Preflight yang sudah diimplementasikan sebelum restart:

1. aktifkan strict shell mode;
2. pastikan script berjalan di repository yang benar;
3. ambil server-side deployment lock;
4. pastikan tidak ada tracked local changes;
5. pastikan Docker Compose dan `curl` tersedia;
6. validasi `BACKEND_ENV` (format, key duplikat, key wajib, nilai wajib);
7. jalankan `docker compose config --quiet` dengan env baru;
8. pastikan PostgreSQL, MinIO, dan API container baseline sedang berjalan;
9. simpan image ID dan image reference API yang aktif;
10. jangan menyentuh container bila preflight gagal.

Verifikasi disk, external network `fabric_test`, material Fabric mount, dan
credential Git tetap menjadi gate Fase 0 di server karena nilainya bergantung
pada instalasi production. Preflight `.env` mengganti file lokal dengan shared
env yang sudah divalidasi dan dapat memulihkan backup bila deployment gagal.

### 7.3 Sinkronisasi source

Urutan yang direncanakan:

1. `git fetch origin main`;
2. periksa `git rev-parse origin/main` sama dengan `EXPECTED_SHA`;
3. pastikan current branch dapat diarahkan ke `main` tanpa konflik;
4. `git checkout main`;
5. `git pull --ff-only origin main`;
6. periksa `git rev-parse HEAD` kembali sama dengan `EXPECTED_SHA`;
7. catat previous SHA dan deployed SHA.

Tidak memakai `git reset --hard`. Local changes menyebabkan deployment berhenti,
bukan dihapus otomatis.

### 7.4 Build dan restart

Urutan rutin:

1. `docker compose config`;
2. build hanya `api-gateway`;
3. jika build gagal, container lama tetap berjalan;
4. setelah build sukses, ganti hanya `api-gateway`;
5. gunakan `--no-deps` agar PostgreSQL, MinIO, dan initializer tidak ikut
   direcreate;
6. tunggu container health;
7. panggil `/healthz` dan `/readyz`;
8. tandai deployment berhasil setelah dua endpoint probe lulus.

Perintah inti tetap setara dengan kebutuhan saat ini:

```sh
docker compose build api-gateway
docker compose up -d --no-deps api-gateway
```

### 7.5 Rollback

Sebelum build, script mencatat image ID yang sedang dipakai container lama.

Jika container baru gagal health/readiness:

1. tandai deployment baru gagal;
2. pulihkan shared env backup;
3. kembalikan tag Compose ke image ID sebelumnya;
4. recreate `api-gateway` dengan `--no-build --no-deps`;
5. tunggu health/readiness image lama;
6. workflow tetap berstatus gagal agar insiden terlihat;
7. kembalikan checkout server ke branch `main` dalam keadaan bersih tanpa
   menghapus commit remote.

Rollback image tidak otomatis membatalkan perubahan schema database. Karena
aplikasi menjalankan GORM `AutoMigrate` saat startup, setiap perubahan schema
yang ikut deployment normal harus additive dan backward-compatible.

---

## 8. Health dan Readiness

### 8.1 Liveness

`GET /healthz` membuktikan proses HTTP hidup dan mengembalikan response minimal:

```json
{"status":"ok"}
```

### 8.2 Readiness

`GET /readyz` pada implementasi awal memastikan dependency database wajib siap:

- PostgreSQL wajib sehat;

Compose tetap menahan startup `api-gateway` sampai PostgreSQL sehat dan
initializer MinIO selesai. Pemeriksaan MinIO/Fabric dan feature flag tetap
menjadi bagian observasi/failure drill karena memaksa koneksi ke layanan
eksternal pada probe readiness dapat membuat rollback terlalu agresif.

Response tidak boleh membocorkan DSN, password, private key, certificate path,
atau isi error internal.

### 8.3 Docker health check

`api-gateway` mendapatkan health check yang memanggil endpoint lokal. Nilai
`start_period`, interval, timeout, dan retries harus memperhitungkan waktu
startup serta `AutoMigrate`.

Deployment belum berhasil hanya karena container berstatus `running`.
Deployment harus menunggu status sehat dan readiness HTTP 200.

---

## 9. Urutan Implementasi dan Gate

## Fase 0 - Verifikasi Server dan Jalur GitHub Actions

### Pekerjaan

1. Verifikasi directory repository server yang benar.
2. Verifikasi remote repository dan branch aktif.
3. Verifikasi `git pull --ff-only origin main` dapat dilakukan menggunakan
   credential read-only server.
4. Verifikasi manual command saat ini benar-benar berhasil.
5. Catat Docker dan Compose version.
6. Catat project name, container, network, dan volume aktif.
7. Verifikasi `.env` production tanpa membuka nilainya.
8. Verifikasi path organisasi Fabric.
9. Verifikasi PostgreSQL dan MinIO sehat.
10. Tentukan akses GitHub Actions ke server: Tailscale atau endpoint SSH.
11. Buat/validasi SSH deployment key.
12. Verifikasi host fingerprint.
13. Jalankan test GitHub Actions non-destruktif: connect, `hostname`, dan exit.
14. Catat image API dan Git SHA aktif sebagai baseline rollback.
15. Inventarisasi nama key `.env` server, `.env.example`, source Go, dan Compose
    tanpa mencatat value.
16. Tetapkan kontrak env canonical dan key mana yang wajib/conditional.
17. Buat GitHub Environment secret `BACKEND_ENV` setelah kontrak disetujui.
18. Uji sync env ke temporary path, validasi, backup, dan restore tanpa
    menjalankan Compose.

### Gate Fase 0

- directory dan server target pasti;
- SSH dari GitHub Actions berhasil;
- host fingerprint cocok;
- Git server dapat fetch `main`;
- Docker/Compose dapat dipakai SSH user;
- stateful service sehat;
- rollback baseline tercatat.
- kontrak key env canonical tersedia;
- `BACKEND_ENV` dapat disinkronkan tanpa value muncul pada log;
- backup dan restore env lulus.

Jika gate gagal, jangan mengubah workflow deployment production.

## Fase 1 - Endpoint Health dan Test

### Pekerjaan

1. Implementasikan `/healthz`.
2. Implementasikan `/readyz`.
3. Tambahkan test success dan dependency failure.
4. Tambahkan Compose health check API.
5. Jalankan:
   - `go test -p 1 ./... -count=1 -timeout=120s`;
   - `go vet -p 1 ./...`;
   - `go build -p 1 ./...`;
   - `git diff --check`.

### Gate Fase 1

- endpoint dan test lulus;
- dependency wajib yang gagal membuat readiness gagal;
- response tidak membocorkan secret.

## Fase 2 - Perkuat `deploy.sh`

### Pekerjaan

1. Tambahkan validation branch/service/SHA.
2. Tambahkan `.dockerignore` dan verifikasi build context tidak membawa `.env`.
3. Tambahkan deployment lock.
4. Tambahkan preflight.
5. Ubah build/restart menjadi khusus `api-gateway`.
6. Tambahkan health wait dan smoke test.
7. Tambahkan pencatatan previous image.
8. Tambahkan rollback.
9. Tambahkan cleanup dan log yang aman.

### Gate Fase 2

- build gagal tidak menghentikan container lama;
- stateful services tidak direcreate;
- SHA yang berbeda ditolak;
- local tracked changes ditolak;
- health failure memicu rollback;
- rollback kembali sehat.

## Fase 3 - Workflow Production Manual

### Pekerjaan

1. Tambahkan `.github/workflows/deploy-production.yml`.
2. Gunakan `workflow_dispatch` saja.
3. Tambahkan main-branch guard.
4. Tambahkan validate job.
5. Tambahkan environment `production`.
6. Tambahkan concurrency.
7. Tambahkan Tailscale bila diperlukan.
8. Tambahkan SSH dengan host fingerprint.
9. Tambahkan secure sync `BACKEND_ENV` sebelum deployment.
10. Jalankan remote `deploy.sh` dengan expected SHA.
11. Tambahkan job summary dan sanitized failure logs.

### Gate Fase 3

- merge ke `main` tidak men-deploy;
- tombol **Run workflow** tersedia;
- feature branch ditolak;
- test gagal menghentikan deployment;
- workflow dapat menjalankan deployment staging dari awal sampai health check.

## Fase 4 - Failure Drill Staging

Skenario wajib:

| ID | Skenario | Hasil yang diharapkan |
|---|---|---|
| T01 | Merge ke `main` | Tidak deploy otomatis |
| T02 | Run dari feature branch | Ditolak |
| T03 | Konfirmasi `false` | Tidak deploy |
| T04 | Test Go gagal | Server tidak disentuh |
| T05 | SSH/Tailscale gagal | Container lama tetap aktif |
| T06 | Host fingerprint berbeda | Workflow berhenti |
| T07 | Server repo kotor | Deployment ditolak tanpa menghapus perubahan |
| T08 | Expected SHA berbeda | Deployment ditolak |
| T09 | `BACKEND_ENV` kosong | Env lama dan container lama tetap aktif |
| T10 | Key env wajib hilang/duplikat | Env baru ditolak sebelum Compose |
| T11 | Compose config gagal dengan env baru | Env sebelumnya dipulihkan |
| T12 | Docker build gagal | Container lama tetap aktif |
| T13 | Container baru gagal start | Rollback image dan env lama |
| T14 | Readiness gagal | Rollback dan workflow gagal |
| T15 | Dua workflow bersamaan | Hanya satu deployment aktif |
| T16 | PostgreSQL/MinIO tidak sehat | Preflight menolak deploy |
| T17 | Disk tidak cukup | Build gagal atau preflight server dihentikan; container lama tetap aktif |
| T18 | Rollback manual | API dan env lama kembali sehat |
| T19 | Secret scan log/artifact | Tidak ada secret pada log/artifact |

### Gate Fase 4

- T01-T19 mempunyai bukti hasil;
- rollback otomatis lulus;
- stateful service dan volume tetap utuh;
- operator lain dapat mengikuti runbook.

## Fase 5 - Production Cutover

### Sebelum deployment

1. Tentukan maintenance window.
2. Bekukan merge sementara.
3. Catat commit SHA target.
4. Ambil backup PostgreSQL.
5. Catat image/container lama.
6. Pastikan rollback sudah diuji.
7. Pastikan GitHub Environment dan secrets siap.
8. Pastikan jalur SSH/Tailscale sehat.

### Pelaksanaan

1. Merge workflow dan script yang telah lulus staging ke `main`.
2. Pastikan merge tidak memicu deploy.
3. Buka GitHub Actions.
4. Pilih `Deploy Backend Production`.
5. Pilih branch `main`.
6. Isi alasan dan centang konfirmasi.
7. Tunggu validate job.
8. Reviewer menyetujui environment production.
9. Pantau remote pull, build, restart, dan health check.
10. Simpan link workflow run.

### Observasi

Periksa segera, +15 menit, +30 menit, dan +60 menit:

- `/healthz` dan `/readyz`;
- container restart count;
- log startup;
- database;
- MinIO dan snapshot worker;
- Fabric/anchoring bila aktif;
- snapshot outbox backlog;
- fungsi API/dashboard read-only;
- penggunaan disk dan memory.

### Gate Fase 5

- exact SHA target aktif;
- API sehat minimal 60 menit;
- tidak ada data persisten hilang;
- tidak ada rollback trigger;
- bukti workflow dan hasil observasi tersimpan.

## Fase 6 - Penutupan Jalur Lama

1. Hapus trigger obsolete `ops-Petrus` setelah workflow baru stabil.
2. Tandai workflow lama deprecated atau hapus melalui PR terpisah.
3. Pastikan hanya workflow production yang menjadi jalur resmi.
4. Rotasi credential lama yang tidak digunakan.
5. Finalisasi runbook dan ownership.

---

## 10. Timeline

Asumsi mulai setelah plan disetujui dan akses server/GitHub tersedia:

| Tanggal target | Fase | Fokus | Estimasi |
|---|---|---|---:|
| 23 Sep 2026 | Fase 0 | Verifikasi server, Git, SSH/Tailscale, Docker, baseline | 0.5-1 hari |
| 24 Sep 2026 | Fase 1 | Health/readiness dan test | 0.5-1 hari |
| 25 Sep 2026 | Fase 2 | Hardening `deploy.sh`, API-only deploy, rollback | 1 hari |
| 28 Sep 2026 | Fase 3 | Workflow production manual GitHub Actions | 0.5-1 hari |
| 29 Sep 2026 | Fase 4 | Failure drill staging dan perbaikan | 1 hari |
| 30 Sep 2026 | Fase 5 | Production cutover dan observasi | 0.5-1 hari |
| 1 Okt 2026 | Fase 6 | Buffer, runbook, cleanup workflow lama | 0.5 hari |

Total realistis: **4-6 hari kerja**, termasuk satu hari buffer untuk masalah
SSH, Tailscale, Docker, atau rollback staging.

Urutan wajib:

```text
Fase 0
  -> Fase 1
      -> Fase 2
          -> Fase 3
              -> Fase 4
                  -> Fase 5
                      -> Fase 6
```

Production tidak boleh dijalankan sebelum failure drill dan rollback staging
lulus.

---

## 11. Risiko dan Mitigasi

| Risiko | Mitigasi | Stop condition |
|---|---|---|
| Branch selain `main` dipilih | Job guard + environment branch policy | Ref bukan `main` |
| `main` berubah setelah workflow mulai | Bandingkan `origin/main` dengan expected SHA | SHA berbeda |
| SSH menuju host salah | Pin host fingerprint | Fingerprint berbeda |
| Tailscale/SSH timeout | Test koneksi sebelum remote deployment | Host tidak terjangkau |
| Repository server mempunyai perubahan | Tolak deploy, jangan reset | Tracked changes ditemukan |
| Git credential server gagal | Deploy key/token read-only dan preflight fetch | Fetch gagal |
| Build image gagal | Build sebelum mengganti container | Build exit non-zero |
| API baru tidak sehat | Health check + rollback image lama | Readiness timeout |
| PostgreSQL/MinIO ikut berubah | Deploy `api-gateway --no-deps` | Compose plan mengubah stateful service |
| Dua deployment bersamaan | GitHub concurrency + server file lock | Lock aktif |
| Migrasi database tidak kompatibel | Additive migration + backup + review | Migration destruktif |
| Secret muncul di log | Environment secrets + masking + sanitized logs | Secret terdeteksi |
| `BACKEND_ENV` tidak lengkap/salah | Validate key dan Compose sebelum restart, backup serta atomic replace | Validation gagal |
| Env baru tidak kompatibel dengan image | Rollback image dan shared env sebagai satu unit | Readiness gagal |

---

## 12. Definition of Done

Status checkbox di bawah membedakan implementasi yang sudah ada di repository
dengan gate staging/production yang masih membutuhkan akses server dan GitHub.

- [x] Merge ke `main` tidak otomatis deploy.
- [x] Tombol **Run workflow** tersedia.
- [x] Hanya branch `main` dapat deploy.
- [x] Workflow memakai GitHub-hosted runner.
- [ ] GitHub Actions terhubung ke server dengan SSH key.
- [x] SSH host fingerprint diverifikasi oleh action.
- [x] Test/vet/build/Compose validation lulus sebelum server disentuh.
- [x] GitHub SHA dibandingkan dengan commit yang ditarik server.
- [x] Operator tidak menjalankan `git pull` manual pada deployment normal.
- [x] Operator tidak menjalankan Docker Compose manual pada deployment normal.
- [x] `.dockerignore` mencegah secret dan file lokal masuk build context.
- [x] Deployment hanya mengganti `api-gateway`.
- [x] PostgreSQL dan MinIO tidak direcreate pada deployment rutin.
- [x] Health/readiness menentukan keberhasilan deployment.
- [x] Hanya satu deployment dapat berjalan.
- [ ] Rollback otomatis lulus di staging.
- [ ] Rollback manual lulus di staging.
- [ ] Secret tidak masuk repository atau log.
- [ ] Seluruh env aplikasi production disimpan pada satu Environment secret `BACKEND_ENV`.
- [ ] Secret SSH/Tailscale tetap terpisah dari env aplikasi.
- [ ] Shared env server dipasang secara atomic dengan permission `0600`.
- [ ] Env sebelumnya dibackup dan dapat dipulihkan bersama rollback image.
- [ ] Kontrak `.env.example`, source Go, Compose, dan production sudah sinkron.
- [ ] Production sehat minimal 60 menit setelah cutover.
- [ ] Runbook dapat dipakai operator lain.
- [ ] Workflow `ops-Petrus` tidak lagi menjadi jalur production.

---

## 13. Hasil Akhir untuk Operator

Setelah implementasi selesai, proses deployment sehari-hari hanya:

1. merge feature ke `main`;
2. buka tab **Actions**;
3. pilih **Deploy Backend Production**;
4. klik **Run workflow** pada branch `main`;
5. isi alasan dan konfirmasi;
6. tunggu test, deployment, dan health check selesai.

GitHub Actions yang akan menjalankan proses server berikut secara otomatis:

```text
git fetch/pull main
    -> build api-gateway
    -> restart api-gateway
    -> health/readiness check
    -> sukses atau rollback
```

Operator tidak perlu membuka terminal server untuk deployment normal.
