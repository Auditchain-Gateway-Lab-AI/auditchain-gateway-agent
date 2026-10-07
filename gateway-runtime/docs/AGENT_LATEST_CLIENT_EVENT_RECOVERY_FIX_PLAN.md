# Rencana Perbaikan Verifikasi Agent Setelah Recovery

## Status Dokumen

- Tanggal: 20 September 2026
- Scope: backend dan kontrak API AuditChain Gateway
- Status: rencana perbaikan; item dalam dokumen ini belum boleh dianggap selesai sebelum seluruh acceptance test lulus
- Di luar scope: perubahan data pada database operasional client

## Ringkasan Masalah

AuditChain mempunyai dua jenis event yang harus dibedakan:

1. **Event operasional client**, yaitu `INSERT`, `UPDATE`, dan `DELETE` yang berasal dari sistem client.
2. **Event sintetis Gateway**, yaitu `RECOVERY` yang dibuat AuditChain Gateway untuk mencatat bahwa pemulihan telah dilakukan.

Setelah recovery, event `RECOVERY` menjadi event terakhir secara keseluruhan. Namun event tersebut bukan kondisi data terbaru yang berasal dari sistem client dan tidak boleh dibandingkan langsung dengan Agent.

Agent harus membandingkan data terkini di database client dengan **event operasional client terakhir**, bukan dengan event `RECOVERY`.

```text
Data terkini di sistem client
            |
            | dibandingkan oleh Agent
            v
Latest Client Event (INSERT/UPDATE/DELETE)
            |
            | dapat dipulihkan dari snapshot jika tampered
            v
Event RECOVERY (catatan terpisah buatan Gateway)
```

## Kesimpulan Audit Saat Ini

Masalah yang ditemukan bukan menunjukkan bahwa proses dekripsi mengubah struktur metadata.

Proses recovery saat ini sudah:

- mengambil exact object version dari MinIO;
- memeriksa checksum ciphertext;
- mendekripsi snapshot;
- merekonstruksi audit log;
- memeriksa hash snapshot;
- memeriksa Merkle Root dan anchor Fabric;
- menulis hasil recovery dalam transaksi;
- membaca ulang hasil recovery;
- menghitung ulang hash setelah recovery.

Jika struktur metadata berubah saat dekripsi atau penulisan, pemeriksaan `snapshot_hash_mismatch` atau `post_recovery_hash_mismatch` seharusnya menggagalkan recovery.

Dua masalah utama yang harus diperbaiki adalah:

1. backend belum mempunyai definisi terpisah antara event terakhir keseluruhan dan event client terakhir;
2. pembandingan metadata Agent masih case-sensitive dan dapat menghasilkan status `matched` palsu.

## Standar Perilaku yang Diinginkan

Setiap resource memiliki dua referensi terakhir:

| Referensi | Definisi | Kegunaan |
|---|---|---|
| `latest_event` | Event terbaru secara keseluruhan, termasuk `RECOVERY` | Menampilkan urutan histori AuditChain |
| `latest_client_event` | Event terbaru yang benar-benar berasal dari client | Menjadi pembanding data terkini melalui Agent |

Contoh setelah recovery:

| Event | Action | `is_latest` | `is_latest_client_event` | Status Agent |
|---|---|---:|---:|---|
| Log client yang dipulihkan | `UPDATE` | false | true | `matched`, `mismatch`, atau `unreachable` |
| Event audit recovery | `RECOVERY` | true | false | `skipped_recovery` |

## Perbaikan 1: Definisikan Asal Event

Backend harus dapat membedakan event client dan event sintetis Gateway.

Untuk kompatibilitas data lama, event client dapat dikenali sementara dengan aturan:

```text
action != RECOVERY
```

Untuk desain jangka panjang, disarankan menambahkan konsep eksplisit seperti:

```text
event_origin = CLIENT | GATEWAY
```

Dengan demikian event sintetis lain pada masa depan tidak perlu dikenali hanya berdasarkan nama action.

## Perbaikan 2: Query Latest Client Event

Tambahkan method repository baru:

```go
GetLatestClientLogByResource(resource, clientID string) (*models.AuditLog, error)
```

Query awal yang disarankan:

```sql
SELECT *
FROM audit_logs
WHERE resource = $1
  AND client_id = $2
  AND UPPER(TRIM(action)) <> 'RECOVERY'
ORDER BY timestamp DESC,
         db_timestamp DESC NULLS LAST,
         log_id DESC
LIMIT 1;
```

Urutan harus deterministik agar dua log dengan timestamp sama tidak menghasilkan pilihan yang berubah-ubah.

Method `GetLatestLogByResource` tetap digunakan untuk event terakhir keseluruhan. Method baru digunakan khusus untuk penentuan pembanding Agent.

## Perbaikan 3: Resource History Menggunakan Dua Indeks

`VerifyResourceHistory` saat ini hanya menganggap elemen terakhir sebagai latest event.

Perilaku baru:

1. cari indeks event terakhir keseluruhan;
2. cari indeks event client terakhir dengan melewati event `RECOVERY`;
3. set `is_latest=true` hanya pada event terakhir keseluruhan;
4. set `is_latest_client_event=true` pada event client terakhir;
5. jalankan Agent hanya pada `is_latest_client_event=true`;
6. beri event `RECOVERY` status `agent_status=skipped_recovery`;
7. event client historis lain tetap `agent_status=skipped_historical`.

Field response yang perlu ditambahkan:

```json
{
  "is_latest": false,
  "is_latest_client_event": true,
  "agent_status": "matched"
}
```

## Perbaikan 4: Samakan Endpoint Verifikasi Tunggal

Endpoint berikut harus memakai definisi latest client event yang sama:

```text
GET /api/dashboard/verify/:log_id
```

Aturannya:

- event `RECOVERY` tidak pernah dikirim ke Agent;
- log client yang dipulihkan tetap dapat diperiksa Agent walaupun ada event `RECOVERY` sesudahnya;
- log client lama yang bukan latest client event tetap `skipped_historical`.

Tanpa perbaikan ini, resource-history dan single-log verification dapat memberikan hasil yang berbeda untuk resource yang sama.

## Perbaikan 5: Normalisasi Nama Field Agent

Metadata AuditChain umumnya menggunakan key lowercase:

```json
{
  "nama": "Ruang ICU",
  "aktif": "1",
  "id_unit": "4026"
}
```

Agent Morbis dapat mengembalikan key uppercase:

```json
{
  "NAMA": "Ruang ICU",
  "AKTIF": "1",
  "ID_UNIT": "4026"
}
```

Lookup case-sensitive menyebabkan field tidak ditemukan dan dilewati. Jika semua field dilewati, backend dapat salah menyatakan `matched`.

Perbaikannya:

1. normalisasi semua key Agent menjadi lowercase;
2. trim whitespace pada key;
3. normalisasi key metadata AuditChain dengan aturan yang sama;
4. deteksi key duplikat setelah normalisasi;
5. bandingkan menggunakan map yang sudah dinormalisasi.

Contoh:

```text
NAMA       -> nama
ID_UNIT    -> id_unit
UPDATED_AT -> updated_at
```

## Perbaikan 6: Field Hilang Harus Menjadi Discrepancy

Field metadata yang tidak ditemukan pada response Agent tidak boleh dilewati diam-diam.

Respons discrepancy yang disarankan:

```json
{
  "field": "nama",
  "in_log": "Ruang ICU",
  "in_agent": "(missing)"
}
```

Aturan pembandingan:

- field tersedia dan nilainya sama: cocok;
- field tersedia tetapi berbeda: mismatch;
- field yang diwajibkan tidak tersedia: mismatch;
- tidak ada satu pun field bermakna yang dapat dibandingkan: `not_comparable`, bukan `matched`.

Backend perlu menghitung jumlah field yang benar-benar dibandingkan. Status `matched` hanya boleh diberikan jika sedikitnya satu field bermakna telah dibandingkan dan seluruhnya cocok.

## Perbaikan 7: Normalisasi Nilai

Nilai log dan Agent dapat mempunyai representasi berbeda tetapi makna sama.

Normalisasi harus mencakup:

- integer dan float yang ekuivalen;
- timestamp RFC3339;
- epoch detik, milidetik, dan mikrodetik;
- zona waktu ke UTC;
- canonical JSON untuk object dan array;
- pembedaan antara `null` dan field yang tidak tersedia;
- boolean yang tidak disamakan sembarangan dengan string.

Toleransi timestamp harus terbatas dan terdokumentasi, misalnya maksimal satu detik untuk perbedaan presisi.

Daftar field yang sengaja diabaikan harus eksplisit. Field identitas yang diabaikan dari metadata tetap harus diverifikasi melalui resource ID pada URL atau response Agent.

## Perbaikan 8: Pisahkan Kontrak Endpoint Agent

Backend saat ini mempunyai konsep verifikasi berdasarkan:

1. primary key resource;
2. `source_record_id` atau audit trail.

Kedua mode tidak boleh menggunakan endpoint yang sama dengan asumsi struktur response berbeda.

Kontrak yang disarankan:

```text
GET /verify/:table/:primary_key
```

Mengembalikan kondisi resource terkini:

```json
{
  "found": true,
  "table": "RUANGAN",
  "id": "81",
  "data": {}
}
```

Jika verifikasi audit trail historis memang dibutuhkan, sediakan endpoint terpisah:

```text
GET /verify-audit/:audit_trail_id
```

Mengembalikan event audit historis tertentu. Sampai kontrak tersebut tersedia, mode resource terkini harus menjadi mode utama.

## Perbaikan 9: Aturan Agent Berdasarkan Action

### INSERT

- Agent harus mengembalikan `found=true`.
- Metadata latest client event dibandingkan dengan data Agent.
- Record tidak ditemukan berarti `mismatch`.

### UPDATE

- Agent harus mengembalikan `found=true`.
- Metadata latest client event dibandingkan dengan data Agent.
- Perbedaan field berarti `mismatch`.

### DELETE

- Agent harus mengembalikan `found=false`.
- Jika record masih tersedia, hasilnya `mismatch`.
- Jika record tidak tersedia, hasilnya `matched`.

### RECOVERY

- Tidak diperiksa langsung ke Agent.
- Statusnya `skipped_recovery`.
- Agent memeriksa latest client event yang menjadi target atau referensi recovery.

## Perbaikan 10: Pisahkan Hasil Pemeriksaan Saat Tampered

Saat hash lokal berbeda, backend harus tetap membedakan hasil masing-masing lapisan:

```json
{
  "integrity_status": "tampered",
  "chain_status": "valid",
  "agent_status": "matched",
  "agent_reference_log_id": "1789...",
  "recovery_status": "not_recovered"
}
```

Interpretasinya:

- `integrity_status=tampered`: isi PostgreSQL Gateway telah berubah;
- `chain_status=valid`: anchor asli masih diakui Fabric;
- `agent_status=matched`: kondisi client masih cocok dengan data asli yang terpercaya;
- recovery dapat dilanjutkan menggunakan snapshot MinIO yang telah divalidasi.

Jika log lokal sudah tampered, identitas resource untuk pemeriksaan Agent sebelum recovery sebaiknya berasal dari snapshot yang sudah lolos preflight. Jangan mempercayai `resource`, `action`, atau ID dari row PostgreSQL yang sudah diketahui rusak.

## Perbaikan 11: Post-Recovery Agent Verification

Setelah transaksi recovery dan post-recovery hash readback berhasil:

1. baca kembali target log yang dipulihkan;
2. pastikan target adalah latest client event yang relevan;
3. jalankan verifikasi Agent terhadap target tersebut;
4. kembalikan hasil Agent secara terpisah;
5. buat event `RECOVERY` dengan `agent_status=skipped_recovery`.

Response yang disarankan:

```json
{
  "status": "SUCCEEDED",
  "post_recovery_integrity": "VALID",
  "post_recovery_agent_status": "matched",
  "agent_reference_log_id": "1789..."
}
```

Jika Agent tidak tersedia:

```json
{
  "status": "SUCCEEDED",
  "post_recovery_integrity": "VALID",
  "post_recovery_agent_status": "unreachable"
}
```

Agent yang tidak tersedia atau mismatch tidak boleh membatalkan recovery yang sudah sah secara MinIO, hash, Merkle Proof, dan Fabric. Kondisi tersebut merupakan status sumber client yang terpisah.

## Perbaikan 12: Kontrak Status API

Empat status berikut harus selalu dipisahkan:

| Field | Makna |
|---|---|
| `integrity_status` | Integritas row PostgreSQL Gateway terhadap hash |
| `chain_status` | Kecocokan Merkle Proof dan anchor Fabric |
| `agent_status` | Kecocokan latest client event dengan data live client |
| `recovery_status` | Status workflow recovery |

Top-level resource response yang disarankan:

```json
{
  "resource": "RUANGAN:81",
  "chain_status": "valid",
  "source_status": "matched",
  "source_reference_log_id": "1789878761589152220",
  "latest_event_log_id": "uuid-recovery",
  "latest_client_event_log_id": "1789878761589152220",
  "logs": []
}
```

Nilai `agent_status` yang didukung:

```text
matched
mismatch
unreachable
not_configured
not_comparable
skipped_historical
skipped_recovery
```

## Perbaikan 13: Kontrak Tampilan Frontend

Frontend hanya mengikuti status backend dan tidak menentukan sendiri apakah data valid.

Tampilan yang disarankan:

- **Data Integrity:** `VALID`, `TAMPERED`, `PENDING`, atau `UNREACHABLE`;
- **Blockchain:** `ANCHORED`, `PENDING`, atau `UNREACHABLE`;
- **Client Source:** `MATCHED`, `MISMATCH`, `UNREACHABLE`, atau `NOT CONFIGURED`;
- **Recovery:** `NOT RECOVERED`, `PENDING`, `RECOVERED`, atau `FAILED`;
- badge **Latest Event** untuk event terakhir keseluruhan;
- badge **Latest Client State** untuk event yang menjadi referensi Agent.

Frontend tidak boleh mengubah seluruh status data menjadi `UNREACHABLE` hanya karena Agent sedang tidak tersedia.

## Perbaikan 14: Pengujian Wajib

Tambahkan unit test pada package `agentverifier` dan integration test pada module audit/recovery.

### Unit Test Agent Verifier

- [ ] Metadata lowercase dan Agent uppercase menghasilkan `matched`.
- [ ] Satu field berbeda menghasilkan `mismatch`.
- [ ] Field Agent hilang menghasilkan discrepancy.
- [ ] Tidak ada field yang dapat dibandingkan menghasilkan `not_comparable`.
- [ ] Timestamp epoch dan RFC3339 yang ekuivalen menghasilkan `matched`.
- [ ] INSERT/UPDATE dengan record tidak ditemukan menghasilkan `mismatch`.
- [ ] DELETE dengan record tidak ditemukan menghasilkan `matched`.
- [ ] DELETE dengan record masih ada menghasilkan `mismatch`.
- [ ] Response Agent tidak valid tidak menghasilkan `matched`.

### Unit Test Audit Service

- [ ] Latest event biasa juga menjadi latest client event.
- [ ] Latest event `RECOVERY` tidak menjadi latest client event.
- [ ] Event client sebelum `RECOVERY` tetap diperiksa Agent.
- [ ] Event `RECOVERY` menghasilkan `skipped_recovery`.
- [ ] Agent unreachable tidak mengubah `chain_status=valid` menjadi unreachable.
- [ ] Single-log dan resource-history memilih reference log yang sama.

### Integration Test Recovery

- [ ] Tamper metadata latest client event terdeteksi Layer 2.
- [ ] Preflight mengambil exact snapshot version.
- [ ] Snapshot dan Fabric lolos pemeriksaan.
- [ ] Target dipulihkan dan hash readback sama.
- [ ] Restored target menjadi `VALID`.
- [ ] Restored target dibandingkan dengan Agent.
- [ ] Event `RECOVERY` dibuat dan tidak dikirim ke Agent.
- [ ] Recovery tetap `SUCCEEDED` ketika Agent unreachable.
- [ ] Client A tidak dapat memeriksa atau memulihkan resource Client B.

## Contoh Skenario End-to-End

### Kondisi Awal

```text
Client DB:      nama = Hindu21
Gateway log:    nama = Hindu21
Fabric hash:    hash(Hindu21)
MinIO snapshot: nama = Hindu21
```

### Setelah Penyerang Mengubah Gateway

```text
Client DB:      nama = Hindu21
Gateway log:    nama = Hindu100
Stored hash:    hash(Hindu21)
Fabric hash:    hash(Hindu21)
MinIO snapshot: nama = Hindu21
```

Hasil pemeriksaan:

```text
Gateway integrity: TAMPERED
Fabric anchor:     VALID
Agent source:      MATCHED terhadap snapshot terpercaya
Recovery:          AVAILABLE
```

### Setelah Recovery

```text
Client DB:           nama = Hindu21
Restored target log: nama = Hindu21
Recovery event:      catatan pemulihan terpisah
```

Hasil akhir:

```text
Restored target integrity: VALID
Blockchain:                VALID
Agent source:              MATCHED
Recovery status:           RECOVERED
RECOVERY event Agent:      SKIPPED_RECOVERY
```

## Urutan Implementasi

Urutan berikut tidak boleh dilompati:

1. [ ] Definisikan `latest_event` dan `latest_client_event`.
2. [ ] Tambahkan `GetLatestClientLogByResource`.
3. [ ] Tambahkan `is_latest_client_event` pada response.
4. [ ] Perbaiki resource-history.
5. [ ] Perbaiki single-log verification.
6. [ ] Normalisasi key Agent secara case-insensitive.
7. [ ] Jadikan field hilang sebagai discrepancy.
8. [ ] Tambahkan deteksi `not_comparable`.
9. [ ] Rapikan normalisasi nilai.
10. [ ] Pisahkan kontrak resource dan audit-trail Agent.
11. [ ] Tambahkan post-recovery Agent verification.
12. [ ] Tambahkan field ringkasan status API.
13. [ ] Tambahkan seluruh unit dan integration test.
14. [ ] Jalankan E2E di staging.
15. [ ] Sesuaikan frontend berdasarkan kontrak API final.

## Definition of Done

Pekerjaan dinyatakan selesai jika:

- event `RECOVERY` tidak pernah diverifikasi langsung ke Agent;
- latest client event tetap diverifikasi setelah recovery;
- uppercase/lowercase key Agent tidak menghasilkan false match;
- field yang hilang tidak diabaikan;
- status `matched` tidak dapat dihasilkan dari nol field yang dibandingkan;
- INSERT, UPDATE, dan DELETE mempunyai aturan Agent yang benar;
- single-log dan resource-history memberikan referensi Agent yang sama;
- Agent offline tidak mengubah integritas Gateway/Fabric;
- recovery tetap fail-closed terhadap MinIO, checksum, hash, Merkle Proof, dan Fabric;
- hasil post-recovery menjelaskan integritas, blockchain, Agent, dan recovery secara terpisah;
- tenant isolation berdasarkan `client_id` tetap berlaku;
- seluruh unit, integration, dan E2E test lulus;
- frontend menampilkan status terpisah tanpa menyimpulkan recovery gagal hanya karena Agent unreachable.

