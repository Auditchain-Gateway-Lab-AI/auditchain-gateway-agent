# Dokumentasi API Admin Panel — AuditChain Gateway Backend

Dokumentasi ini menjelaskan endpoint API **Admin Panel** pada AuditChain Gateway Backend yang digunakan untuk manajemen klien (perusahaan/tenant), konfigurasi stream Kafka, pendaftaran otomatis via Tailscale Agent Installer, serta ringkasan dashboard.

---

## 1. Ringkasan Endpoint

| Method | Endpoint | Auth | Deskripsi |
| :--- | :--- | :---: | :--- |
| `POST` | `/api/admin/clients` | JWT Admin | Mendaftarkan klien/perusahaan baru |
| `GET` | `/api/admin/clients` | JWT Admin | Mengambil daftar semua klien terdaftar |
| `PATCH` | `/api/admin/clients/:id/toggle` | JWT Admin | Mengaktifkan / menonaktifkan status klien |
| `DELETE` | `/api/admin/clients/:id` | JWT Admin | Menghapus klien terdaftar |
| `POST` | `/api/admin/kafka-config` | JWT Admin | Mendaftarkan konfigurasi stream Kafka untuk klien |
| `GET` | `/api/admin/kafka-configs` | JWT Admin | Mengambil daftar semua konfigurasi Kafka |
| `PATCH` | `/api/admin/kafka-config/:id/toggle` | JWT Admin | Mengaktifkan/Menonaktifkan stream Kafka |
| `DELETE` | `/api/admin/kafka-config/:id` | JWT Admin | Menghapus konfigurasi Kafka milik klien |
| `GET` | `/api/admin/summary` | JWT Admin | Mengambil statistik ringkasan dashboard |
| `GET` | `/api/admin/clients/:id/users` | JWT Admin | Mengambil daftar pengguna/user milik klien |
| `POST` | `/api/admin/clients/:id/users` | JWT Admin | Menambahkan pengguna baru pada klien |
| `DELETE` | `/api/admin/users/:id` | JWT Admin | Menghapus pengguna milik klien |
| `POST` | `/api/admin/clients/:id/agent-config` | JWT Admin | Mendaftarkan konfigurasi Agent (Lapis 3) untuk klien |
| `GET` | `/api/admin/clients/:id/agent-config` | JWT Admin | Melihat konfigurasi Agent milik klien |
| `DELETE` | `/api/admin/clients/:id/agent-config` | JWT Admin | Menghapus konfigurasi Agent milik klien |
| `GET` | `/api/admin/clients/:id/agent-ping` | JWT Admin | Melakukan test ping ke Agent klien |
| `POST` | `/api/agent/tailscale-key` | Public | Menghasilkan Auth Key temporer Tailscale untuk `install.sh` |
| `POST` | `/api/agent/telemetry` | Public | Public callback telemetri dari `install.sh` |
| `GET` | `/install.sh` | Public | Download file script installer 1-command |

## 2.1 Recovery Snapshot MinIO

Audit log baru dapat memiliki snapshot terenkripsi pada MinIO Recovery Vault.
PostgreSQL menyimpan index `object_key`, `version_id`, checksum, dan status snapshot;
payload recovery tidak disimpan sebagai plaintext di MinIO.
Endpoint recovery aktif hanya jika `RECOVERY_ENABLED=true`.

| Method | Endpoint | Auth | Keterangan |
| :--- | :--- | :---: | :--- |
| `GET` | `/api/dashboard/recovery/incidents` | JWT | Daftar insiden tamper |
| `GET` | `/api/dashboard/recovery/incidents/:id` | JWT | Detail insiden |
| `GET` | `/api/dashboard/recovery/resources/:resource/versions` | JWT | Daftar versi snapshot terverifikasi |
| `GET` | `/api/dashboard/recovery/requests` | JWT | Daftar request recovery |
| `GET` | `/api/dashboard/recovery/requests/:id` | JWT | Detail request recovery |
| `POST` | `/api/dashboard/recovery/requests` | JWT | Meminta recovery dengan idempotency key |
| `POST` | `/api/dashboard/recovery/requests/:id/execute` | JWT Client | Verifikasi PostgreSQL/MinIO/Merkle/Fabric lalu pulihkan audit log |

Implementasi MVP hanya menulis kembali audit log target di Gateway setelah snapshot versi yang dipilih lolos dekripsi, hash, Merkle proof, dan verifikasi anchor Fabric. Menulis kembali record pada database operasional klien belum diaktifkan; jalur tersebut memerlukan Agent adapter dengan izin write-back yang eksplisit.
Body request recovery: `incident_id`, `selected_log_id`, `reason`, dan `idempotency_key`. Untuk MVP, `selected_log_id` wajib sama dengan `log_id` pada incident target.

---

## 2. Skema Database Utama

### Tabel `clients`
| Kolom | Tipe Data | Keterangan |
| :--- | :--- | :--- |
| `id` | `varchar(36)` | Primary Key (UUID) |
| `company_name` | `varchar(100)` | Nama perusahaan/klien |
| `api_key_prefix` | `varchar(20)` | Prefix API Key yang disamarkan (contoh: `ak_live_38`) |
| `api_key_hash` | `varchar(255)` | Hash SHA-256 dari API Key |
| `status` | `varchar(20)` | Status klien (`active` / `inactive` / `pending_setup`) |
| `actor_field` | `varchar(100)` | Mapping field kustom untuk aktor |
| `fallback_actor_field` | `varchar(100)` | Fallback field aktor |
| `action_field` | `varchar(100)` | Mapping field kustom untuk aksi |
| `resource_field` | `varchar(100)` | Mapping field kustom untuk resource |

### Tabel `agent_configs`
| Kolom | Tipe Data | Keterangan |
| :--- | :--- | :--- |
| `id` | `varchar(36)` | Primary Key (UUID) |
| `client_id` | `varchar(36)` | Foreign Key ke `clients.id` |
| `agent_server_url` | `varchar(255)` | URL Agent (misal: `http://100.x.x.x:8081`) |
| `tailscale_ip` | `varchar(45)` | IP Tailscale Mesh VPN milik VPS Klien |
| `hostname` | `varchar(100)` | Hostname VPS Klien |
| `is_active` | `boolean` | Status keaktifan Agent |

### Tabel `client_kafka_configs`
| Kolom | Tipe Data | Keterangan |
| :--- | :--- | :--- |
| `id` | `varchar(36)` | Primary Key (UUID) |
| `client_id` | `varchar(36)` | Foreign Key ke `clients.id` |
| `topic_prefix` | `varchar(100)` | Prefix topic Kafka (contoh: `morbis_simrs.`) |
| `kafka_brokers` | `varchar(255)` | Alamat broker Kafka (contoh: `100.x.x.x:9092`) |
| `source_system` | `varchar(100)` | Nama sistem sumber data |
| `is_active` | `boolean` | Status keaktifan stream Kafka |

---

## 3. Detail Endpoint Baru & Agent Auto-Registration

### A. Request Tailscale Auth Key (`POST /api/agent/tailscale-key`)
Digunakan oleh `install.sh` di VPS Klien untuk meminta Auth Key sementara (sekali pakai) dari OAuth Tailscale API secara otomatis.

* **Method**: `POST`
* **URL**: `http://localhost:8080/api/agent/tailscale-key`
* **Request Body**:
  ```json
  {
    "api_key_prefix": "ak_live_abcdef123"
  }
  ```
* **Response Sukses (`200 OK`)**:
  ```json
  {
    "auth_key": "tskey-auth-kXXXXX-XXXXXXXXXXXX"
  }
  ```

---

### B. Telemetri Agent & Auto-Registration (`POST /api/agent/telemetry`)
Digunakan oleh `install.sh` setelah VPS Klien terhubung ke VPN Tailscale. Endpoint ini otomatis mengisikan/membuat data di tabel `clients`, `agent_configs`, dan `client_kafka_configs`.

* **Method**: `POST`
* **URL**: `http://localhost:8080/api/agent/telemetry`
* **Request Body**:
  ```json
  {
    "api_key_prefix": "ac_live_vps_test",
    "kafka_brokers": "100.109.120.82:9092",
    "agent_server_url": "http://100.109.120.82:8081",
    "hostname": "vps-client-rs",
    "status": "pending_setup"
  }
  ```
* **Response Sukses (`200 OK`)**:
  ```json
  {
    "message": "Telemetri agent berhasil diproses",
    "client_id": "263b8ab8-781b-4f9e-a81d-6161a0f8b1a2",
    "status": "pending_setup"
  }
  ```
  > 💡 Jika API Key tidak ditemukan / baru, sistem membuat Record Draft dengan status `pending_setup` dan `company_name`: `"Auto Registered"`.

---

### C. Download Script Installer (`GET /install.sh`)
Mengunduh script otomatisation installer 1-command.

* **Cara Penggunaan di VPS Klien**:
  ```bash
  curl -fsSL http://<GATEWAY_IP>:8082/api/install.sh | sudo bash -s -- http://<GATEWAY_IP>:8082 <CLIENT_API_KEY>
  ```

---

## 4. Alur Kerja Auto-Registration & Tailscale Unattended Setup

```
┌───────────────────────────┐                ┌───────────────────────────┐
│     VPS KLIEN (Ubuntu)    │                │      GATEWAY BACKEND      │
└─────────────┬─────────────┘                └─────────────┬─────────────┘
              │                                            │
              │  1. GET /install.sh                        │
              ├───────────────────────────────────────────>│
              │  2. Download Script `install.sh`           │
              │<───────────────────────────────────────────┤
              │                                            │
              │  3. POST /api/agent/tailscale-key          │
              ├───────────────────────────────────────────>│ (OAuth Tailscale API)
              │  4. Return Temp AuthKey                    │
              │<───────────────────────────────────────────┤
              │                                            │
              │  5. Exec `tailscale up --authkey=...`      │
              │     (VPS Klien masuk Mesh VPN)             │
              │                                            │
              │  6. POST /api/agent/telemetry              │
              ├───────────────────────────────────────────>│ (Simpan Telemetri &
              │  7. Return 200 OK (Status: Pending Setup)  │  Auto Insert 3 Tabel)
              │<───────────────────────────────────────────┤
```
