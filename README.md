# auditchain-agent

Agent AuditChain di sisi client untuk verifikasi database Oracle dan direct
recovery yang sempit, terautentikasi, ber-policy, serta idempoten. Recovery
write disabled secara default dan tidak menggantikan finalisasi Gateway melalui
CDC, Merkle proof, dan Fabric.

## Cara Kerja

```
[Gateway] → GET /verify/:table/:id → [Agent] → [Oracle Client DB]
[Gateway] → POST /recover/:table/:id → [Agent] → [Oracle Client DB]
[Oracle Client DB] → Debezium/CDC → [Gateway] → Merkle/Fabric confirmation
```

Recovery menghasilkan perubahan row normal agar dapat ditangkap Debezium/CDC;
Agent tidak mengirim callback final recovery ke Gateway.

## Prasyarat

- Go 1.25+
- Akses ke database sumber Oracle
- `AGENT_VERIFY_TOKEN` untuk read endpoint
- `AGENT_RECOVERY_TOKEN`, `AGENT_CLIENT_ID`, dan policy lokal hanya bila
  recovery pilot diaktifkan

## Konfigurasi

Salin dan sesuaikan `config.yml` serta environment. `config.yml` hanya
menyimpan konfigurasi non-secret; gunakan `.env.example` sebagai daftar
variabel runtime:

```yaml
tables:
  - name: pasien
    source_system: SIMRS-Pasien
```

Untuk recovery, copy `recovery-policy.example.yml` menjadi policy lokal yang
sudah direview. Policy harus exact dan tidak boleh memuat credential, password,
token, secret, binary, LOB, generated, atau virtual column.

## POC Modul Audit Read-Only

`internal/modules/agent_audit` menyediakan endpoint opsional untuk membaca
log Gateway dari PostgreSQL Besu dan membandingkan Merkle root dengan anchor
Fabric. Modul ini mati secara default (`AGENT_AUDIT_MODULE_ENABLED=false`) dan
berjalan pada listener Agent yang sama; modul ini bukan pengganti seluruh
backend Besu dan tidak memindahkan consumer Kafka.

- `GET /api/agent/offchain-data/:resource` mengambil log terbaru untuk resource.
- `GET /api/agent/offchain-data?from=<RFC3339>&to=<RFC3339>` mengambil rentang
  log dengan batas jumlah dan ukuran respons.
- `POST /api/agent/verify-merkle-root` mengevaluasi anchor Fabric secara
  read-only (`EvaluateTransaction`) lalu membandingkan root.

Semua route memerlukan `Authorization: Bearer <AGENT_VERIFY_TOKEN>`; endpoint
offchain juga mensyaratkan `X-Client-ID` yang sama dengan
`AGENT_AUDIT_CLIENT_ID`. Saat POC diaktifkan, gunakan role PostgreSQL terpisah
yang hanya memiliki `SELECT` pada `audit_logs`, URL TLS yang sesuai, serta
sertifikat Fabric yang dipasang sebagai secret read-only. Kode hanya membuka
transaksi PostgreSQL read-only dan tidak memanggil Kafka, endpoint tulis
Fabric, atau operasi mutasi database. `.env.example` mencantumkan nama
konfigurasinya; jangan menaruh kredensial nyata di Git. Compose belum
menyiapkan network PostgreSQL Besu maupun mount sertifikat Fabric untuk modul
ini, jadi jangan ubah flag menjadi `true` sebelum wiring deployment tersebut
ditinjau dan diuji terpisah.

## Menjalankan

```bash
# Install dependencies
go mod tidy

# Jalankan dengan config default (config.yml)
go run main.go

# Atau tentukan path config sendiri
go run main.go /path/to/config.yml
```

## Build Binary

```bash
go build -o auditchain-agent main.go
./auditchain-agent
```

## Syarat Database Sumber

Direct recovery tidak membuat tabel baru di database client. Tabel pilot harus
memiliki primary key stabil dan mapping kolom yang sama dengan projection yang
dipakai Gateway untuk canonical state/hash.
