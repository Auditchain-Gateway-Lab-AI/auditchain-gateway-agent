# Runbook Deployment Production AuditChain

> **Status:** server Besu yang saat ini menjadi target adalah **development**.
> Workflow aktifnya adalah **Deploy Backend Development** dan panduan utamanya
> berada di [Development Deployment Runbook](DEVELOPMENT_DEPLOYMENT_RUNBOOK.md).
> Dokumen ini dipertahankan sebagai referensi hardening/cutover production di
> masa depan; jangan menjalankannya untuk server development saat ini.

Runbook ini adalah panduan operasional untuk workflow
`.github/workflows/deploy-production.yml`. Jalur normal memakai tombol **Run
workflow**; operator tidak perlu menjalankan `git pull` atau Docker Compose
secara manual di server.

## 1. Prasyarat satu kali

### Server Besu

Pastikan user deployment mempunyai:

- checkout repository pada path yang akan disimpan sebagai
  `BACKEND_PROJECT_DIR`;
- akses `git fetch` ke remote repository;
- akses Docker/Compose tanpa login root;
- akses ke external network `fabric_test`;
- path organisasi Fabric yang dipasang read-only pada `docker-compose.yml`;
- container `auditchain-postgres` yang sudah berjalan; MinIO tidak diperlukan
  untuk deployment canonical `RECOVERY_MODE=agent_direct`;
- utilitas `bash`, `curl`, `awk`, `grep`, `flock`, `install`, `git`, dan Docker
  Compose.

Buat direktori shared env di luar checkout jika belum ada. Script akan membuat
struktur ini otomatis pada deployment pertama:

```text
/home/besu/auditchain/shared/backend.env
/home/besu/auditchain/shared/env-backups/
/home/besu/auditchain/shared/backend-deploy.lock
```

User SSH harus dapat membaca dan menulis direktori tersebut. Jangan menyimpan
private key, password, atau nilai secret di repository.

### GitHub Environment

Buat environment repository bernama `production`. Disarankan menambahkan
required reviewer dan membatasi deployment branch/environment ke `main`.

Tambahkan secret berikut pada environment `production`:

| Secret | Isi |
|---|---|
| `BACKEND_ENV` | Satu multiline secret berisi seluruh `.env` production |
| `BACKEND_PROJECT_DIR` | Absolute path checkout di server, misalnya `/home/besu/auditchain/middleware-AuditChain-Gateway` |
| `PRODUCTION_SSH_HOST` | Host/IP atau hostname Tailscale server |
| `PRODUCTION_SSH_PORT` | Port SSH; boleh dikosongkan karena workflow memakai `22` |
| `PRODUCTION_SSH_USERNAME` | User deployment non-root |
| `PRODUCTION_SSH_PRIVATE_KEY` | Private key khusus GitHub Actions |
| `PRODUCTION_SSH_HOST_FINGERPRINT` | Fingerprint host SSH format SHA256 |
| `TAILSCALE_OAUTH_CLIENT_ID` | Wajib hanya jika `PRODUCTION_NETWORK=tailscale` |
| `TAILSCALE_OAUTH_SECRET` | Wajib hanya jika `PRODUCTION_NETWORK=tailscale` |

Jika memakai Tailscale, tambahkan repository/environment variable
`PRODUCTION_NETWORK=tailscale` dan pastikan tag OAuth dapat mencapai host SSH.
Jika host dapat dijangkau langsung, variable tersebut tidak perlu dibuat.

`BACKEND_ENV` wajib memuat minimal key non-kosong berikut:

```dotenv
APP_ENV=production
PORT=8080
DB_DSN=...
JWT_SECRET=...
```

Tambahkan key Fabric, Redis, recovery, snapshot, Agent, dan Tailscale yang
memang dipakai deployment production. Untuk cutover direct client-DB, gunakan
`RECOVERY_MODE=agent_direct` hanya setelah Agent write contract sudah live dan
staging E2E lulus. Nilai secret tidak boleh dicetak pada log workflow. Setiap
key harus hanya muncul sekali; `deploy.sh` menolak baris malformed, key
duplikat, dan nilai kosong untuk key wajib. Jika `.env` server lama memiliki
konfigurasi yang berulang, pilih satu nilai canonical sebelum menyalinnya ke
`BACKEND_ENV`.

## 2. Sebelum klik Run workflow

1. Pastikan Pull Request feature sudah di-merge ke `main`.
2. Pastikan `main` tidak sedang menerima merge lain.
3. Pastikan tidak ada perubahan tracked lokal di checkout server.
4. Pastikan PostgreSQL, external Fabric network, volume, dan material Fabric
   tersedia. Siapkan MinIO hanya jika deployment memang memakai override
   `docker-compose.snapshot.yml` untuk mode `snapshot_legacy`.
5. Bila ini cutover besar, ambil backup database dan catat image/container lama.

## 3. Menjalankan deployment

1. Buka tab **Actions** di GitHub.
2. Pilih **Deploy Backend Production**.
3. Klik **Run workflow**.
4. Pilih branch `main`.
5. Isi `deployment_reason`.
6. Ubah `confirm_production` menjadi `true`.
7. Jika environment meminta approval, reviewer menyetujui deployment.

Urutan job adalah:

```text
guard
  -> validate (format, vet, test, build, Compose, deploy.sh)
      -> upload BACKEND_ENV sementara melalui SCP
          -> SSH ke server
              -> fetch/pull exact github.sha
              -> validasi dan atomic install shared env
              -> compose build api-gateway
              -> restart api-gateway saja
              -> /healthz + /readyz
              -> sukses atau rollback image/env sebelumnya
```

Workflow production tidak mempunyai trigger `push`, sehingga merge ke `main`
tidak otomatis merestart server.

## 4. Verifikasi setelah sukses

Di log workflow, pastikan terlihat:

- expected SHA sama dengan SHA yang di-checkout server;
- `docker compose config --quiet` berhasil;
- build `api-gateway` berhasil;
- PostgreSQL tidak direcreate; MinIO hanya diverifikasi jika legacy snapshot
  override dipakai;
- health check dan readiness lulus;
- temporary env pada `/tmp` sudah dihapus.

Lanjutkan observasi segera, 15, 30, dan 60 menit:

```bash
docker compose ps api-gateway
docker compose logs --tail=200 api-gateway
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

Setelah cutover direct, verifikasi minimal:

1. Agent `GET /health` dan `GET /verify/:table/:id` dapat dijangkau dari
   container Gateway menggunakan verify token tenant yang benar;
2. endpoint recovery Agent memakai recovery token terpisah dan menolak SQL atau
   tabel/kolom di luar allowlist;
3. preflight Gateway berhasil tanpa MinIO untuk incident pilot;
4. satu recovery pilot menghasilkan `recovery_events` terpisah, event CDC
   client, status `SUCCEEDED`, dan incident `RESOLVED` setelah verifikasi
   Fabric;
5. Agent/Fabric/CDC failure drill dilakukan sebelum traffic recovery diperluas.

Jika Agent tidak dapat dijangkau, jangan menganggap data tampered. Status
`agent_status=unreachable` hanya berarti pemeriksaan source belum dapat
diselesaikan; integritas Gateway/Fabric harus tetap dibaca dari dimensinya
sendiri.

Jangan menyalin output yang berisi secret ke tiket atau chat.

## 5. Jika workflow gagal

- **Guard gagal**: workflow dijalankan dari branch selain `main`, atau
  konfirmasi belum `true`.
- **Validate gagal**: server belum disentuh; perbaiki kode/test terlebih dahulu.
- **SSH/SCP gagal**: cek route jaringan, Tailscale, username, key, port, dan
  fingerprint. Container lama tidak diubah.
- **Expected SHA berbeda**: `main` berubah setelah workflow dimulai; jalankan
  ulang dari SHA terbaru.
- **Repository kotor**: simpan perubahan lokal di server dan ulangi setelah
  checkout bersih. Script tidak melakukan reset atau menghapus perubahan.
- **Env ditolak/Compose config gagal**: env sebelumnya dipulihkan dari backup.
- **Build gagal**: container lama tetap dipertahankan.
- **Readiness gagal**: script mengembalikan image dan env sebelumnya lalu
  menunggu health/readiness rollback.

Status final dan link run dicatat pada summary workflow. Temporary file env
dibersihkan oleh cleanup step best-effort; jika server tidak dapat dijangkau
saat cleanup, hapus file `/tmp/auditchain-backend-env-<run_id>` secara aman
setelah koneksi pulih.

## 6. Rollback manual darurat

Gunakan rollback manual hanya setelah deployment otomatis gagal atau setelah
keputusan operator. Catat terlebih dahulu backup env dan image aktif:

```bash
cd /home/besu/auditchain/middleware-AuditChain-Gateway
ls -l /home/besu/auditchain/shared/env-backups
docker inspect --format '{{.Image}} {{.Config.Image}}' auditchain-api
```

Pulihkan file env backup yang dipilih ke
`/home/besu/auditchain/shared/backend.env` dengan mode `0600`, pastikan `.env`
repository tetap symlink ke file tersebut, lalu gunakan image lama yang sudah
dicatat untuk menjalankan kembali hanya service `api-gateway`. Setelah itu
jalankan `/healthz` dan `/readyz` dan simpan bukti hasilnya.

Jangan melakukan `git reset --hard`, menghapus volume, atau merecreate
PostgreSQL sebagai langkah rollback standar. MinIO legacy juga tidak perlu
disentuh oleh deployment direct.
