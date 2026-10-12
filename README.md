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

`internal/modules/agent_audit` menyediakan endpoint opsional pada Agent yang
meneruskan permintaan baca ke API Gateway Besu. PostgreSQL off-chain dan Fabric
tetap berada di Besu; Agent tidak membuka koneksi langsung ke keduanya. Modul
mati secara default (`AGENT_AUDIT_MODULE_ENABLED=false`) dan tidak memindahkan
consumer Kafka atau proses anchoring.

- `GET /api/agent/offchain-data/:resource` mengambil log terbaru untuk resource.
- `GET /api/agent/offchain-data?from=<RFC3339>&to=<RFC3339>` mengambil rentang
  log dengan batas jumlah dan ukuran respons.
- `POST /api/agent/verify-merkle-root` meminta Besu membandingkan Merkle root
  dengan anchor Fabric.

Panggilan ke listener Agent memerlukan `Authorization: Bearer
<AGENT_VERIFY_TOKEN>`; Agent meneruskan permintaan ke Besu menggunakan
`GATEWAY_URL`, API key tenant `AUDITCHAIN_API_KEY`, dan
`AGENT_AUDIT_CLIENT_ID`. API key mengautentikasi identitas tenant di Besu.
Jangan menaruh nilai rahasia nyata di Git.

Untuk uji terpisah tanpa mengganti container Agent utama, tersedia
`docker-compose.agent-audit-poc.yml`. Service uji memakai port `9091`, bind ke
`127.0.0.1` secara default, dan recovery dimatikan. Sebelum menjalankannya:

1. Isi `GATEWAY_URL`, `AUDITCHAIN_API_KEY`, `AGENT_AUDIT_CLIENT_ID`, dan token
   Agent di `.env` lokal. API key harus milik client ID yang sama.
2. Pastikan backend Besu yang dituju mendaftarkan rute `/api/agent/*` dengan
   autentikasi API key tenant; rute tanpa autentikasi tidak boleh dipakai.
3. Validasi konfigurasi lalu jalankan service uji:

   ```bash
   docker-compose -f docker-compose.agent-audit-poc.yml config >/dev/null
   docker-compose -f docker-compose.agent-audit-poc.yml up -d --build
   docker inspect auditchain-agent-audit-poc \
     --format 'status={{.State.Status}} health={{.State.Health.Status}}'
   docker logs --tail=80 auditchain-agent-audit-poc
   curl -i --max-time 10 http://127.0.0.1:9091/health
   ```

Service ini hanya menguji proxy baca Agent → API Besu. Ia tidak mengambil alih
off-chain storage, Fabric, consumer Kafka, normalisasi CDC, atau anchoring.
Setelah uji selesai, hapus hanya container uji dengan:

```bash
docker-compose -f docker-compose.agent-audit-poc.yml down
```

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
