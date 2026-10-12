# Gateway Mini PC: batas API klien

Admin Portal dan seluruh rute `/api/admin/*` tetap dilayani di Besu.
Gateway Mini PC hanya boleh mengekspos rute API yang dipakai Client Portal.
Jangan menjalankan consumer kedua atau memindahkan PostgreSQL/Fabric dari Besu.

Kontrak Client Portal saat ini (status kode V2, belum di-deploy):

- Sudah dibuat sebagai modul API klien: `POST /api/auth/login`,
  `GET /api/auth/me`, `GET /api/dashboard/stats`, dan
  `GET /api/dashboard/inventory`.
- Belum dipindah: `GET /api/dashboard/verification-runs/latest`,
  `GET /api/dashboard/verify-range/estimate`, dan
  `GET /api/dashboard/verify-range/client` (tahap verifikasi).
- Rute `/api/dashboard/recovery/*` tetap tahap recovery tersendiri.

Entrypoint preview lokal `internal/gatewayapi` sekarang mendaftarkan subset
endpoint tersebut tanpa memulai consumer Kafka. Secara default ia menolak
start. Preview memerlukan `GATEWAY_CLIENT_API_PREVIEW_ENABLED=true`, alamat
loopback eksplisit melalui `GATEWAY_CLIENT_API_BIND` (contoh
`127.0.0.1:18081`), `CLIENT_API_DB_DSN` untuk akun PostgreSQL Besu khusus
baca, dan `JWT_SECRET` yang sesuai kontrak autentikasi yang berlaku.
Tidak ada Compose/deployment baru untuk preview ini.

Daftar ini adalah batas migrasi, bukan klaim bahwa API V2 sudah aktif.
Jangan arahkan Client Portal ke subset API ini sebelum rute yang dipakai
portal lengkap, autentikasi dan tenant-scope diuji end-to-end, dan cutover
server HTTP direncanakan. Admin Portal dan API admin Besu tidak diubah.
Endpoint verifikasi/recovery tetap menunggu keputusan kontrak dan pengujian.
