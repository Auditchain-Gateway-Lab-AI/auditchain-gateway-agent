# Runbook Deployment Development AuditChain

Workflow development ini memakai tombol **Run workflow** di GitHub Actions.
Merge ke `main` tidak otomatis mengubah server Besu development, dan operator
tidak perlu lagi menjalankan `git pull` atau Docker Compose secara manual.

## Prasyarat server

User SSH deployment harus memiliki:

- checkout repository pada path yang disimpan di `BACKEND_PROJECT_DIR`;
- akses `git fetch`/`git pull` ke remote repository;
- akses Docker Compose tanpa login root;
- utilitas `bash`, `curl`, `awk`, `grep`, `flock`, `install`, dan `git`;
- izin membaca/menulis direktori shared di luar checkout.

Pada deployment pertama, script membuat struktur berikut relatif terhadap parent
checkout (atau sesuai `DEPLOY_SHARED_DIR`):

```text
shared/backend.env
shared/env-backups/
shared/backend-deploy.lock
```

## Secret dan variable GitHub Actions

Secret repository yang sudah dibuat dapat tetap digunakan. Nama yang dipakai
workflow saat ini adalah:

| Secret | Isi |
|---|---|
| `BACKEND_ENV` | Seluruh isi `.env` development sebagai satu multiline secret |
| `BACKEND_PROJECT_DIR` | Absolute path checkout di server Besu development |
| `PRODUCTION_SSH_HOST` | Host/IP/Tailscale address server development |
| `PRODUCTION_SSH_PORT` | Port SSH, biasanya `22` |
| `PRODUCTION_SSH_USERNAME` | User SSH deployment non-root |
| `PRODUCTION_SSH_PRIVATE_KEY` | Private key khusus GitHub Actions |
| `PRODUCTION_SSH_HOST_FINGERPRINT` | Fingerprint host SSH format `SHA256:...` |
| `TAILSCALE_OAUTH_CLIENT_ID` | Diisi jika runner perlu masuk Tailscale |
| `TAILSCALE_OAUTH_SECRET` | Diisi jika runner perlu masuk Tailscale |

Nama `PRODUCTION_*` dipertahankan agar secret yang sudah Anda input tidak perlu
dibuat ulang; server tujuan tetap development. Jika host tidak dapat dijangkau
langsung, buat Actions variable `PRODUCTION_NETWORK` bernilai `tailscale`.
Jika host publik atau dapat dijangkau tanpa Tailscale, variable tersebut boleh
dikosongkan.

`BACKEND_ENV` minimal harus memiliki key unik dan tidak kosong berikut:

```dotenv
APP_ENV=development
PORT=8080
DB_DSN=...
JWT_SECRET=...
```

Nilai lain seperti Fabric, Redis, recovery, snapshot, dan Agent mengikuti
kebutuhan server development. Compose utama tidak memulai MinIO karena pilot
direct client-DB tidak membutuhkannya. Untuk pilot direct client-DB, gunakan:

```dotenv
RECOVERY_ENABLED=true
RECOVERY_MODE=agent_direct
RECOVERY_CDC_TIMEOUT_SECONDS=120
```

Mode `snapshot_legacy` tetap dapat dijalankan untuk rollback/kompatibilitas
dengan `docker-compose.snapshot.yml`; mode tersebut membutuhkan konfigurasi
MinIO yang lengkap dan tidak boleh menjadi dependency deployment direct.

Pastikan setiap tenant pilot mempunyai `agent_url`, `verify_token`, dan
`recovery_token` yang berbeda sesuai scope-nya. `recovery_token` tidak boleh
dimasukkan ke repository atau dicetak ke log. Agent harus sudah menyediakan
`POST /recover/:table/:record_id` sebelum mode direct diaktifkan.
Jangan menaruh secret lain di repository atau mencetak isi secret ke log.

## Menjalankan deployment

1. Pastikan Pull Request fitur sudah di-merge ke `main`.
2. Buka tab **Actions**.
3. Pilih workflow **Deploy Backend Development**.
4. Klik **Run workflow**.
5. Pilih branch `main`.
6. Isi alasan deployment.
7. Centang konfirmasi deployment development.

Urutan workflow:

```text
guard
  -> validate (format, vet, test, build, Compose, deploy.sh)
      -> SCP mengirim BACKEND_ENV sementara ke server
          -> SSH menjalankan deploy.sh
              -> fetch/pull exact github.sha dari main
              -> validasi dan install env secara atomic
              -> compose build api-gateway
              -> compose up api-gateway
              -> /healthz + /readyz
```

Workflow hanya memiliki trigger `workflow_dispatch`; merge ke `main` tidak
langsung melakukan deployment.

## Verifikasi setelah sukses

Di log workflow, pastikan terlihat SHA yang diharapkan sama dengan SHA yang
checkout di server, Compose config berhasil, build selesai, dan health/readiness
lulus. Pemeriksaan tambahan di server:

```bash
cd /path/yang-disimpan/di/BACKEND_PROJECT_DIR
docker compose ps api-gateway
docker compose logs --tail=200 api-gateway
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

Untuk smoke test direct setelah health/readiness lulus, lakukan preflight pada
satu incident staging, pastikan response memuat `fabric_root`,
`desired_state_hash`, dan `source_status`, lalu jalankan recovery hanya pada
resource pilot. Verifikasi bahwa request berhenti di `APPLIED_AWAITING_CDC`
sebelum event Debezium diterima dan baru menjadi `SUCCEEDED` setelah event
recovery ter-hash serta ter-anchor. Jangan menguji write recovery pada data
produksi.

File environment sementara pada `/tmp` dihapus oleh cleanup step. File canonical
yang dipakai Compose berada di `shared/backend.env` dengan mode `0600` dan
`.env` pada checkout menjadi symlink ke file tersebut.

## Troubleshooting singkat

- Guard gagal: workflow bukan dijalankan dari `main` atau konfirmasi belum dicentang.
- Validate gagal: server belum disentuh; perbaiki kode/test terlebih dahulu.
- SSH/SCP gagal: periksa host, port, username, private key, fingerprint, dan
  apakah Tailscale variable diperlukan.
- Env ditolak: cek `APP_ENV`, key wajib, key duplikat, dan baris malformed.
- Repository kotor: simpan perubahan tracked lokal di server sebelum mencoba lagi.
- Health/readiness gagal: lihat log `api-gateway`; script akan mencoba rollback
  image dan env sebelumnya jika baseline container tersedia.
