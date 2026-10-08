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

Gateway juga menggunakan read-only `GET /verify-audit/:audit_trail_id` untuk
membaca event asli dari tabel historis client (default `AUDIT_TRAIL`). Endpoint
ini terpisah dari `GET /verify/:table/:id`, yang membaca row operasional saat
ini. Gateway dapat memakai event historis tersebut untuk memperbaiki metadata
audit log tanpa MinIO setelah hash, Merkle proof, dan Fabric anchor cocok.

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
