# Gateway V2: batas pemindahan data plane

Alur yang dituju: SIMRS → Debezium/Kafka di Mini PC → Gateway worker di Mini PC
→ PostgreSQL off-chain dan Hyperledger Fabric di Besu. Kode yang dipindah
**bukan** server PostgreSQL, peer/orderer Fabric, atau seluruh backend Besu.

Entrypoint khusus Gateway ada di `internal/gatewayworker/main.go`. `main.go`
di akar repo tetap executable Agent sisi klien dan tidak diubah. Worker V2
memakai `internal/engine/kafkaconsumer` (termasuk normalisasi CDC yang memang
berada di consumer), `hasher`, `aggregator`, `stats`, helper Merkle di
`internal/backendsupport/crypto`, model database terkait, dan client Fabric di
`internal/blockchain/fabric.go`. Paket `internal/blockchain/agentverifier`
beserta dependensi kecilnya dipertahankan untuk tahap verifikasi/recovery,
namun tidak dipanggil oleh worker ini.

`internal/consumer` dan `internal/publisher` yang sudah ada sebelumnya milik
Agent klien; keduanya tidak dipanggil oleh worker Gateway V2 dan tidak boleh
dianggap sebagai consumer Gateway kedua.

Tidak disalin ke worker: server API/router/dashboard, `engine/normalizer` yang bukan
jalur Kafka aktif, snapshot store/MinIO, snapshot worker, tamper scanner,
recovery engine, migrasi skema backend, maupun seluruh `internal/modules`.
Modul `internal/modules/clientapi` adalah tahap API klien **terpisah dari worker**:
saat ini baru memuat login, pembacaan profil, statistik dashboard dan trend,
serta inventaris tabel klien dengan larangan login admin serta query inventaris
yang dibatasi `client_id`. Entrypoint preview HTTP `internal/gatewayapi`
mendaftarkan subset tersebut hanya pada alamat loopback dan dengan opt-in
eksplisit; ia tidak memulai Kafka consumer. Belum ada deploy atau server API
V2 yang melayani portal, sehingga portal masih memakai Gateway yang telah
berjalan sebelumnya. Batas endpoint lanjutan
tercatat di `CLIENT_API_SCOPE.md`; API admin dan Admin Portal tetap di Besu.
Worker membuka PostgreSQL melalui `DB_DSN` tanpa `AutoMigrate` atau DDL.
Fabric client mengirim transaksi ke jaringan Fabric di Besu; ledger tetap di
Besu. Source V2 ini **belum** menyediakan semua API HTTP yang dipakai portal, jadi
belum bisa menggantikan seluruh Gateway yang saat ini berjalan.

Pengaman cutover: worker tidak berjalan kecuali
`GATEWAY_BACKGROUND_WORKERS_ENABLED=true` dan
`GATEWAY_V2_CUTOVER_CONFIRMED=true`, start offset Kafka bernilai
`earliest`/`latest`, dan `GATEWAY_PIPELINE_CUTOFF_UTC` berisi timestamp RFC3339
yang dipilih setelah pemeriksaan data. Cutoff membatasi pemrosesan baris lama,
bukan mengganti offset Kafka. Sebelum mengaktifkan worker ini, operator harus
memastikan consumer lama berhenti dan satu group Kafka hanya memiliki worker
yang direncanakan. Jangan menyalakan keduanya untuk uji coba.

Validasi lokal hanya membuktikan kompilasi dan tes unit; belum menguji koneksi
Mini PC ke PostgreSQL/Fabric Besu, kesesuaian skema, autentikasi, atau alur
CDC→anchor end-to-end. Penyatuan kembali dengan API/dashboard dan deployment
Compose perlu tahap terpisah tanpa menambah pipeline paralel.
