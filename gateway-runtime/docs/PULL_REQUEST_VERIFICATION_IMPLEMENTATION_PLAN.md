# Implementation Plan Pull Request Verification

## 1. Informasi Dokumen

| Item | Nilai |
|---|---|
| Proyek | AuditChain Gateway Backend |
| Tanggal perencanaan | 22 September 2026 |
| Target implementasi | Selesai hari ini, 22 September 2026 |
| Branch saat analisis | `nama-fitur-baru` |
| Commit baseline | `4bf47be2b854baf39bd2394b49043d7160302020` |
| Target branch PR | `main` |
| Workflow baru | `.github/workflows/pr-check.yml` |
| Status dokumen | Siap diimplementasikan setelah persetujuan |

Dokumen ini menjadi urutan kerja resmi untuk menambahkan Pull Request
Verification. Pengerjaan tidak boleh melompati gate antarfase. Jika suatu gate
gagal, penyebabnya diperbaiki pada fase tersebut sebelum melanjutkan.

---

## 2. Tujuan

Pull Request Verification harus memastikan perubahan yang akan masuk ke `main`:

1. mengikuti format dan aturan dasar Go;
2. tidak merusak build atau unit test;
3. tidak membuat `go.mod` dan `go.sum` tidak sinkron;
4. tidak membuat dokumentasi Swagger tertinggal dari kontrak API;
5. tetap dapat dibangun sebagai Docker image;
6. tidak merusak Docker Compose atau script operasional;
7. tidak membawa secret ke repository;
8. tidak memperkenalkan vulnerability dependency yang sudah dapat dideteksi;
9. menghasilkan satu status akhir yang dapat diwajibkan pada Ruleset `main`.

Alur target:

```text
feature branch
    -> buka atau update Pull Request ke main
    -> GitHub Actions menjalankan PR Verification
    -> seluruh pemeriksaan lulus
    -> review manusia dan approval
    -> merge ke main
    -> deployment belum berjalan otomatis
    -> operator menjalankan Deploy Backend Development secara manual
```

PR Verification hanya memutuskan apakah perubahan layak di-merge. Workflow ini
tidak melakukan deployment.

---

## 3. Batas Scope

### 3.1 Termasuk dalam implementasi hari ini

- workflow `pull_request` untuk target branch `main`;
- deteksi ruang lingkup file yang berubah;
- pemeriksaan format, module integrity, vet, test, coverage, dan build Go;
- pemeriksaan konsistensi Swagger;
- validasi Dockerfile dan Docker Compose;
- validasi sintaks shell dan workflow GitHub Actions;
- secret scanning;
- vulnerability scanning dependency Go;
- satu aggregate status `All PR Checks Passed`;
- Job Summary yang menjelaskan hasil pemeriksaan;
- konfigurasi Ruleset `main` setelah workflow terbukti berjalan;
- dokumentasi cara membaca hasil dan menangani kegagalan.

### 3.2 Tidak termasuk dalam implementasi hari ini

- SSH, SCP, Tailscale, atau akses ke server Besu;
- penggunaan `BACKEND_ENV` atau secret deployment lainnya;
- deployment otomatis setelah merge;
- menjalankan PostgreSQL, MinIO, Kafka, atau Fabric milik server development;
- end-to-end test langsung ke jaringan Hyperledger Fabric;
- perubahan logic aplikasi untuk menaikkan coverage;
- penambahan migration framework baru;
- penghapusan validasi dari workflow deployment yang sudah ada.

Integration test PostgreSQL untuk `AutoMigrate` dicatat sebagai penguatan tahap
berikutnya karena membutuhkan fixture database dan refactor kecil agar kegagalan
migration dapat diuji tanpa `log.Fatal`.

---

## 4. Kondisi Repository Saat Perencanaan

Repository saat ini adalah satu backend Go, bukan monorepo multi-runtime.
Komponen relevan yang ditemukan:

- Go `1.25.0` dari `go.mod`;
- Gin, GORM, PostgreSQL, MinIO, Kafka, dan Hyperledger Fabric Gateway;
- unit test pada health, JWT, recovery, audit, snapshot store, snapshot worker,
  hasher, dan tamper scanner;
- Swagger hasil generate pada `docs/docs.go`, `docs/swagger.json`, dan
  `docs/swagger.yaml`;
- Dockerfile multi-stage dan `docker-compose.yml`;
- `deploy.sh` dan sejumlah script operasional shell;
- aplikasi menjalankan GORM `AutoMigrate` serta SQL PostgreSQL tambahan saat
  startup;
- workflow deployment manual sudah menjalankan format, vet, test, build,
  Compose validation, dan validasi `deploy.sh`.

Konsekuensi desain:

1. Check React, NestJS, Python, dan Hardhat seperti pada workflow RekaKarbon
   tidak relevan untuk repository ini.
2. Fabric tidak boleh menjadi dependency eksternal pada setiap PR. Logic Fabric
   diverifikasi melalui compile dan unit test/mock.
3. Docker Compose tidak dijalankan penuh pada GitHub runner karena mempunyai
   external network `fabric_test` dan bind mount material organisasi Fabric dari
   host server.
4. Workflow deployment tetap mengulangi pemeriksaan kritis sebagai pertahanan
   terakhir sampai PR Verification stabil.

---

## 5. Keputusan Keamanan Workflow

1. Trigger menggunakan `pull_request`, bukan `pull_request_target`.
2. Workflow tidak memakai GitHub Environment `development` atau `production`.
3. Workflow tidak membaca repository secret apa pun.
4. Permission dibatasi menjadi:

   ```yaml
   permissions:
     contents: read
     pull-requests: read
   ```

5. `pull-requests: read` diperlukan untuk pemeriksaan metadata/commit PR,
   termasuk secret scanning, tanpa memberi izin menulis komentar atau mengubah
   PR.
6. Semua third-party action harus dipin ke full commit SHA. Tag versi hanya
   ditulis sebagai komentar agar supply-chain action tidak berubah diam-diam.
7. Tidak ada `write` permission, token buatan pengguna, SSH key, atau credential
   server dalam workflow.
8. Workflow dari fork tetap aman karena tidak mempunyai akses ke deployment
   secrets dan tidak menggunakan `pull_request_target`.
9. Log tidak boleh mencetak isi environment atau file sensitif.

---

## 6. Trigger, Concurrency, dan Commit yang Diuji

### 6.1 Trigger

```yaml
on:
  pull_request:
    branches:
      - main
    types:
      - opened
      - synchronize
      - reopened
      - ready_for_review
  workflow_dispatch:
```

`workflow_dispatch` disediakan untuk diagnostic rerun oleh maintainer. Trigger
`push` tidak diperlukan karena tujuan workflow ini adalah menjadi gate sebelum
merge.

Workflow tidak memakai `paths-ignore` pada level trigger. Alasannya, required
status check harus tetap muncul pada PR dokumentasi. Optimasi dilakukan melalui
job `detect_changes`, bukan dengan melewatkan seluruh workflow.

### 6.2 Concurrency

Satu PR hanya boleh mempunyai satu run aktif:

```yaml
concurrency:
  group: pr-check-${{ github.event.pull_request.number || github.ref }}
  cancel-in-progress: true
```

Commit lama dibatalkan saat commit baru dipush agar hasil lama tidak dianggap
sebagai keputusan terbaru.

### 6.3 Commit checkout

- Validasi umum memakai merge commit sintetis PR yang dibuat GitHub sehingga
  konflik dengan `main` dapat terlihat sebelum merge.
- Secret scan memakai history yang cukup (`fetch-depth: 0`) agar rentang commit
  PR dapat diperiksa.
- Workflow tidak mengubah branch dan tidak melakukan push.

---

## 7. Arsitektur Job

```text
detect_changes
    |
    +--> go_quality
    +--> unit_tests
    +--> api_contract
    +--> container_and_scripts
    +--> security
             |
             v
       all_checks_passed
```

Seluruh job pemeriksaan berjalan paralel setelah `detect_changes`. Job
`all_checks_passed` memakai `if: always()` dan baru sukses bila seluruh job yang
wajib berstatus `success` atau memang sah berstatus `skipped` berdasarkan jenis
perubahan.

---

## 8. Detail Setiap Job

### 8.1 `detect_changes` - Detect Changed Scope

Tujuan:

- menentukan job berat yang benar-benar perlu dijalankan;
- tetap menghasilkan status akhir untuk PR dokumentasi;
- menampilkan daftar kategori perubahan pada Job Summary.

Kategori:

| Output | Pola perubahan utama | Job yang dipicu |
|---|---|---|
| `go` | `**/*.go`, `go.mod`, `go.sum` | quality, test, contract, container |
| `api` | `main.go`, router, handler, model API, `docs/swagger*` | API contract |
| `container` | `Dockerfile`, `docker-compose.yml`, `.dockerignore` | container validation |
| `scripts` | `deploy.sh`, `scripts/**/*.sh` | shell validation |
| `workflow` | `.github/workflows/**` | workflow lint |
| `docs_only` | hanya Markdown/gambar dokumentasi | security dan final gate |

Implementasi boleh memakai path-filter action yang dipin ke full commit SHA.
Jika action tersebut tidak dapat diverifikasi, fallback adalah `git diff
--name-only` dengan base SHA dan head SHA dari event PR.

Gate:

- base dan head commit berhasil ditentukan;
- output boolean tersedia untuk seluruh kategori;
- daftar nama file tidak memuat isi file atau secret.

### 8.2 `go_quality` - Go Formatting, Module, Vet, dan Build

Kondisi: berjalan bila perubahan memengaruhi Go/module.

Urutan pemeriksaan:

1. checkout merge commit PR;
2. setup Go dari `go.mod` dengan cache module/build;
3. periksa format:

   ```bash
   unformatted="$(gofmt -l .)"
   test -z "$unformatted"
   ```

4. verifikasi checksum dependency:

   ```bash
   go mod verify
   ```

5. jalankan `go mod tidy`, lalu pastikan tidak mengubah `go.mod`/`go.sum`:

   ```bash
   go mod tidy
   git diff --exit-code -- go.mod go.sum
   ```

6. static analysis:

   ```bash
   go vet -p 1 ./...
   ```

7. build seluruh package:

   ```bash
   go build -p 1 ./...
   ```

8. build binary sesuai target Linux server:

   ```bash
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/auditchain-gateway ./main.go
   ```

Gate:

- tidak ada file yang dilaporkan `gofmt`;
- module graph terverifikasi dan `tidy` tidak menghasilkan diff;
- vet dan kedua build sukses.

### 8.3 `unit_tests` - Backend Unit Tests dan Coverage

Kondisi: berjalan bila perubahan memengaruhi Go/module.

Perintah wajib:

```bash
go test -p 1 ./... -count=1 -timeout=120s -coverprofile=coverage.out -covermode=atomic
go tool cover -func=coverage.out
```

Hasil `coverage.out` diunggah sebagai artifact non-secret dengan retention
pendek. Hari pertama tidak menerapkan angka minimum coverage karena baseline
aktual harus diukur terlebih dahulu. Menetapkan threshold tanpa baseline dapat
mengunci semua PR meskipun tidak menurunkan kualitas.

Race detector dijalankan sebagai sub-step terpisah:

```bash
go test -race -p 1 ./... -count=1 -timeout=180s
```

Keputusan gate race detector:

- langsung menjadi blocking bila baseline pertama lulus stabil;
- bila ada race lama yang tidak berasal dari PR, hasil didokumentasikan dan
  dibuat backlog terpisah; check normal tetap blocking;
- `continue-on-error` hanya boleh dipakai sementara dan harus diberi issue serta
  tanggal penghapusan.

Gate:

- seluruh unit test lulus;
- tidak ada timeout atau panic;
- coverage artifact terbentuk;
- race detector lulus atau mempunyai pengecualian sementara yang terdokumentasi.

### 8.4 `api_contract` - Verify Swagger Contract

Kondisi: berjalan pada perubahan Go/API/Swagger.

Urutan:

1. setup Go;
2. jalankan generator Swagger dengan versi yang dipin dan sama dengan dependency
   repository;
3. bandingkan hasil generate dengan file ter-track:

   ```bash
   go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go
   git diff --exit-code -- docs/docs.go docs/swagger.json docs/swagger.yaml
   ```

Gate:

- generator berhasil;
- hasil generate tidak menghasilkan diff;
- bila gagal, pengembang harus memperbarui annotation atau commit ulang ketiga
  file Swagger, bukan mematikan check.

### 8.5 `container_and_scripts` - Docker, Compose, Shell, Workflow

Kondisi:

- Docker build berjalan bila Go atau container berubah;
- shell syntax berjalan bila script berubah;
- workflow lint berjalan bila workflow berubah;
- Compose config tetap diperiksa untuk perubahan runtime terkait.

Docker image build:

```bash
docker build --tag auditchain-gateway:pr-check .
```

Compose validation memakai `.env` kosong sementara dan harus membersihkannya
setelah selesai:

```bash
trap 'rm -f .env' EXIT
: > .env
docker compose config --quiet
```

Workflow tidak menjalankan `docker compose up` karena runner tidak memiliki
external Fabric network, Tailscale IP server, atau material organisasi Fabric.

Shell syntax:

```bash
find . -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
```

Workflow lint:

- jalankan `actionlint` versi yang dipin;
- validasi seluruh `.github/workflows/*.yml`;
- error pada expression, dependency job, event, atau struktur YAML harus
  menggagalkan PR.

Gate:

- Docker image berhasil dibangun;
- Compose dapat dirender tanpa error;
- seluruh shell script lolos `bash -n`;
- seluruh workflow lolos `actionlint`.

### 8.6 `security` - Secret dan Dependency Vulnerability Scan

Job ini selalu berjalan, termasuk pada PR dokumentasi.

Secret scanning:

- checkout dengan `fetch-depth: 0`;
- gunakan Gitleaks action yang dipin ke full commit SHA;
- scan rentang perubahan PR;
- komentar otomatis dinonaktifkan agar permission tetap read-only;
- false positive harus diselesaikan dengan allowlist yang sempit, disertai
  alasan, fingerprint/path, dan review. Dilarang menonaktifkan rule secara luas.

Dependency vulnerability scanning:

```bash
go run golang.org/x/vuln/cmd/govulncheck@<PINNED_VERSION> ./...
```

Versi aktual ditentukan dan dipin saat implementasi setelah memeriksa release
resmi. `@latest` tidak boleh dipakai pada workflow final.

Gate:

- tidak ada secret baru yang terdeteksi;
- tidak ada vulnerability reachable yang belum diterima melalui keputusan
  risiko terdokumentasi;
- job hanya memakai `GITHUB_TOKEN` otomatis dengan read permission.

### 8.7 `all_checks_passed` - Aggregate Required Check

Job ini:

- memakai `needs` terhadap semua job sebelumnya;
- memakai `if: always()` agar tetap muncul ketika job lain gagal atau dibatalkan;
- menerima `skipped` hanya bila sesuai output `detect_changes`;
- gagal bila satu pemeriksaan wajib berstatus `failure` atau `cancelled`;
- menulis ringkasan kategori perubahan dan status setiap job ke
  `$GITHUB_STEP_SUMMARY`;
- mempunyai display name stabil: `All PR Checks Passed`.

Ruleset hanya perlu mewajibkan status ini. Individual job tetap terlihat untuk
diagnosis, tetapi perubahan path atau penambahan job tidak mengharuskan Ruleset
diubah setiap kali.

---

## 9. File yang Direncanakan Berubah

| File | Tindakan | Tujuan |
|---|---|---|
| `.github/workflows/pr-check.yml` | Tambah | Workflow utama PR Verification |
| `docs/PULL_REQUEST_VERIFICATION_IMPLEMENTATION_PLAN.md` | Tambah | Rencana dan timeline resmi |
| `README.md` | Ubah minimal setelah workflow lulus | Dokumentasikan PR gate dan link plan/runbook |
| `.gitleaks.toml` | Opsional, hanya jika diperlukan | Allowlist false positive yang sangat spesifik |

File yang tidak direncanakan berubah:

- `.github/workflows/deploy-production.yml`;
- `deploy.sh`;
- source code aplikasi;
- `.env` atau environment secret;
- konfigurasi server Besu.

Jika implementasi menemukan bahwa source perlu diubah hanya agar check dapat
lulus, perubahan tersebut harus dipisah atau dijelaskan sebagai baseline fix;
tidak boleh disisipkan diam-diam ke workflow PR.

---

## 10. Urutan Implementasi dan Gate

### Fase 0 - Freeze Scope dan Baseline

Pekerjaan:

1. pastikan working tree dan perubahan yang sudah ada diketahui;
2. catat branch, HEAD, dan `origin/main`;
3. inventarisasi test, Swagger, Dockerfile, Compose, shell script, serta workflow;
4. jalankan baseline command yang sama dengan workflow deployment;
5. catat kegagalan lingkungan lokal secara terpisah dari kegagalan source code;
6. pastikan tidak ada secret yang akan dipakai oleh PR workflow.

Gate Fase 0:

- scope workflow disetujui;
- tidak ada file pengguna yang tertimpa;
- baseline dan keterbatasan runner terdokumentasi.

### Fase 1 - Workflow Skeleton dan Change Detection

Pekerjaan:

1. buat `.github/workflows/pr-check.yml`;
2. tambahkan trigger, permission, concurrency, timeout, dan nama job stabil;
3. implementasikan `detect_changes` beserta output kategori;
4. tambahkan placeholder dependency graph;
5. pastikan tidak ada environment atau secrets deployment.

Gate Fase 1:

- YAML valid;
- dependency graph tidak siklik;
- semua job mempunyai timeout;
- permission hanya read-only;
- trigger hanya PR ke `main` dan manual diagnostic.

### Fase 2 - Go Quality, Test, Coverage, dan Build

Pekerjaan:

1. implementasikan `go_quality`;
2. implementasikan `unit_tests`;
3. upload coverage artifact dengan retention pendek;
4. uji race detector;
5. pastikan error format mencetak daftar file, bukan hanya exit code 1;
6. gunakan Go version dari `go.mod` agar tidak terjadi version drift.

Gate Fase 2:

- format, module integrity, vet, unit test, dan build lulus pada baseline;
- coverage report dapat dibaca;
- keputusan blocking/non-blocking race detector tercatat.

### Fase 3 - API Contract, Docker, Compose, dan Script

Pekerjaan:

1. implementasikan Swagger regeneration check;
2. implementasikan Docker build;
3. implementasikan Compose config dengan file env sementara;
4. implementasikan `bash -n` untuk seluruh shell script;
5. implementasikan `actionlint` untuk seluruh workflow;
6. pastikan file `.env` sementara selalu dihapus.

Gate Fase 3:

- Swagger tidak drift;
- Docker image dapat dibangun;
- Compose dan seluruh script valid;
- PR workflow dapat memvalidasi dirinya sendiri.

### Fase 4 - Security Checks

Pekerjaan:

1. pin Gitleaks action ke full commit SHA;
2. konfigurasi scan PR dengan komentar dinonaktifkan;
3. berikan `pull-requests: read` tanpa permission write;
4. pin dan jalankan `govulncheck`;
5. klasifikasikan setiap temuan sebagai valid, false positive, atau existing
   baseline;
6. buat allowlist hanya bila bukti false positive lengkap.

Gate Fase 4:

- secret scan berjalan tanpa 403 permission;
- tidak ada secret valid dalam diff;
- vulnerability result mempunyai keputusan yang dapat diaudit;
- tidak ada secret deployment terekspos ke PR.

### Fase 5 - Aggregate Gate dan Job Summary

Pekerjaan:

1. implementasikan `all_checks_passed`;
2. tangani kombinasi `success`, `skipped`, `failure`, dan `cancelled`;
3. tulis Job Summary yang ringkas;
4. pastikan nama status `All PR Checks Passed` tidak berubah;
5. uji docs-only PR dan Go-code PR secara logis.

Gate Fase 5:

- aggregate gagal jika salah satu required check gagal;
- aggregate sukses untuk docs-only bila check relevan lulus;
- status final selalu muncul pada PR.

### Fase 6 - Local Validation dan Review Diff

Pekerjaan:

1. jalankan parser YAML;
2. jalankan `actionlint` bila tersedia lokal;
3. jalankan `git diff --check`;
4. periksa seluruh `uses:` sudah dipin;
5. cari penggunaan `secrets.`, `environment:`, SSH, SCP, dan Tailscale yang tidak
   diperbolehkan;
6. review diff hanya pada file scope;
7. pastikan tidak ada `.env`, coverage output, atau binary masuk staging.

Gate Fase 6:

- local static validation lulus;
- diff hanya berisi perubahan yang direncanakan;
- tidak ada secret atau artifact lokal ter-track.

### Fase 7 - Live PR Run dan Failure Drill

Pekerjaan:

1. commit workflow pada feature branch;
2. push dan buka/update PR ke `main`;
3. amati setiap job GitHub Actions sampai selesai;
4. perbaiki masalah nyata pada runner;
5. rerun sampai satu run penuh hijau;
6. lakukan failure drill aman pada branch sementara atau commit uji:
   - format Go sengaja salah;
   - unit test sengaja gagal;
   - Swagger sengaja dibuat drift;
   - sintaks workflow/script sengaja salah;
7. pastikan masing-masing kegagalan memblokir aggregate gate;
8. pulihkan commit uji dan pastikan run kembali hijau.

Failure drill tidak boleh dilakukan pada `main` dan tidak boleh memasukkan
secret sungguhan.

Gate Fase 7:

- satu live PR run penuh sukses;
- failure drill membuktikan PR benar-benar diblokir;
- commit terakhir kembali bersih dan hijau.

### Fase 8 - Ruleset Main dan Dokumentasi

Pekerjaan:

1. buka GitHub Settings -> Rulesets untuk branch `main`;
2. wajibkan Pull Request sebelum merge;
3. wajibkan minimal satu approval;
4. aktifkan dismiss stale approvals setelah commit baru;
5. wajibkan semua conversation terselesaikan;
6. tambahkan required status check `All PR Checks Passed`;
7. blokir force push dan branch deletion;
8. dokumentasikan alur pada README;
9. lakukan verifikasi akhir menggunakan PR, bukan direct push.

Gate Fase 8:

- PR gagal tidak dapat di-merge;
- PR hijau dan sudah disetujui dapat di-merge;
- merge tidak otomatis men-deploy;
- deployment manual tetap tersedia setelah merge.

---

## 11. Timeline Pengerjaan Hari Ini

Timeline menggunakan WIB pada 22 September 2026 dan dimulai setelah plan
disetujui.

| Waktu WIB | Fase | Hasil yang harus tersedia |
|---|---|---|
| 14:00-14:20 | Fase 0 | Baseline, scope, dan file target dikunci |
| 14:20-14:50 | Fase 1 | Skeleton workflow, permission, concurrency, change detection |
| 14:50-15:50 | Fase 2 | Go quality, test, coverage, race, dan build |
| 15:50-16:35 | Fase 3 | Swagger, Docker, Compose, shell, dan workflow lint |
| 16:35-17:20 | Fase 4 | Gitleaks dan `govulncheck` terpasang dan dipin |
| 17:20-17:50 | Fase 5 | Aggregate gate dan Job Summary selesai |
| 17:50-18:20 | Fase 6 | YAML/action lint/diff/security review lokal selesai |
| 18:20-18:45 | Fase 7A | Commit, push, dan PR live dibuat/diperbarui |
| 18:45-19:45 | Fase 7B | Live run pertama, diagnosis, dan perbaikan runner |
| 19:45-20:20 | Fase 7C | Failure drill serta successful rerun |
| 20:20-20:45 | Fase 8 | Ruleset `main` dikonfigurasi dan diverifikasi |
| 20:45-21:00 | Penutupan | README, hasil akhir, dan handoff operator |
| 21:00-22:00 | Buffer | Dipakai hanya untuk download/action issue atau rerun CI |

Target utama adalah seluruh kode workflow selesai, live run hijau, dan Ruleset
aktif pada hari yang sama. Jika GitHub mengalami outage atau akses admin
Ruleset tidak tersedia, implementasi repository tetap diselesaikan hari ini dan
blocker eksternal dicatat secara eksplisit; status tidak boleh diklaim selesai
sebelum live run serta Ruleset benar-benar diverifikasi.

Urutan wajib:

```text
Fase 0 -> Fase 1 -> Fase 2 -> Fase 3 -> Fase 4
       -> Fase 5 -> Fase 6 -> Fase 7 -> Fase 8
```

---

## 12. Matriks Kegagalan dan Respons

| Gejala | Kemungkinan penyebab | Respons |
|---|---|---|
| `gofmt` gagal | File Go belum diformat | Jalankan `gofmt -w` hanya pada file terkait |
| `go mod tidy` menghasilkan diff | Dependency metadata tidak sinkron | Review lalu commit `go.mod`/`go.sum` |
| `go vet` gagal | Static issue pada source | Perbaiki source, jangan abaikan global |
| Unit test gagal | Regression atau test tidak deterministik | Reproduksi test package spesifik dan perbaiki |
| Race detector gagal | Data race pada worker/service | Isolasi test dan perbaiki sinkronisasi |
| Swagger drift | Annotation dan docs berbeda | Generate ulang dengan versi yang dipin dan review diff |
| Docker build gagal | Build context/dependency/binary gagal | Reproduksi `docker build` lokal |
| Compose config gagal | Env substitution atau schema salah | Periksa Compose dengan dummy env, tanpa server secret |
| Shell lint gagal | Sintaks Bash rusak | Jalankan `bash -n` pada file yang disebut |
| Actionlint gagal | YAML/expression/dependency job salah | Perbaiki workflow sebelum push berikutnya |
| Gitleaks 403 | `pull-requests: read` tidak tersedia | Perbaiki least-privilege permission |
| Gitleaks menemukan secret | Credential ter-commit | Hapus dari history PR dan rotasi bila valid |
| `govulncheck` gagal | Vulnerability reachable | Upgrade dependency atau dokumentasikan keputusan risiko |
| Aggregate skipped | Kondisi `if`/`needs` salah | Pastikan `if: always()` dan evaluasi seluruh result |
| Semua job hijau tetapi merge bebas | Ruleset belum aktif | Tambahkan required status dan verifikasi dengan PR |

---

## 13. Risiko dan Mitigasi

| Risiko | Mitigasi | Stop condition |
|---|---|---|
| Workflow PR dapat membaca deployment secret | Jangan gunakan environment/secrets dan hindari `pull_request_target` | Ada akses secret/SSH/Tailscale |
| Third-party action berubah | Pin semua action ke full commit SHA | Masih ada tag mutable tanpa alasan |
| Check wajib tidak muncul pada docs-only PR | Jangan gunakan trigger-level `paths-ignore` | Aggregate tidak muncul |
| Job yang skip membuat aggregate palsu sukses | Validasi output change detection dan status `needs` | Failure/cancel diterima sebagai sukses |
| CI terlalu lama | Job paralel, caching, dan conditional execution | Durasi normal konsisten di atas 10 menit |
| Coverage threshold mengunci baseline | Rekam baseline dahulu | Threshold ditambah tanpa data |
| Fabric/server membuat PR flaky | Gunakan mock; pindahkan real integration ke workflow manual | PR membutuhkan server development |
| False positive Gitleaks disembunyikan luas | Allowlist sempit dan terdokumentasi | Rule global dimatikan |
| Vulnerability lama langsung memblokir semua PR | Audit baseline, perbaiki atau buat penerimaan risiko berjangka | Temuan diabaikan tanpa owner/tanggal |
| Workflow deploy dan PR check berbeda | Gunakan command yang sama untuk format/vet/test/build | Main lulus PR tetapi gagal validasi dasar saat deploy |

---

## 14. Rollback Implementasi CI

Workflow ini tidak mengubah runtime server. Rollback hanya menyentuh GitHub CI:

1. nonaktifkan required status `All PR Checks Passed` pada Ruleset hanya bila
   workflow rusak dan memblokir semua PR;
2. perbaiki atau revert file `.github/workflows/pr-check.yml` melalui PR;
3. aktifkan kembali required status setelah satu live run hijau;
4. jangan menonaktifkan seluruh Ruleset atau membuka direct push sebagai solusi
   permanen;
5. deployment manual tetap tidak terpengaruh selama rollback CI.

---

## 15. Definition of Done

- [ ] `.github/workflows/pr-check.yml` tersedia pada repository.
- [ ] Workflow hanya berjalan untuk PR target `main` dan manual diagnostic.
- [ ] Workflow tidak memakai deployment secret, SSH, SCP, atau Tailscale.
- [ ] Permissions hanya `contents: read` dan `pull-requests: read`.
- [ ] Semua third-party action dipin ke full commit SHA.
- [ ] Change detection menghasilkan kategori yang benar.
- [ ] `gofmt`, module integrity, vet, test, coverage, dan build berjalan.
- [ ] Swagger drift check berjalan.
- [ ] Docker build dan Compose config berjalan.
- [ ] Seluruh shell script dan workflow tervalidasi.
- [ ] Gitleaks berjalan tanpa permission error.
- [ ] `govulncheck` mempunyai hasil dan keputusan yang jelas.
- [ ] Aggregate status bernama `All PR Checks Passed` selalu muncul.
- [ ] Aggregate gagal ketika satu required check gagal/dibatalkan.
- [ ] Docs-only PR tetap memperoleh required status.
- [ ] Satu live PR run penuh berhasil.
- [ ] Failure drill membuktikan check memblokir regression.
- [ ] Ruleset `main` mewajibkan `All PR Checks Passed`.
- [ ] Minimal satu approval dan conversation resolution diwajibkan.
- [ ] Merge ke `main` tetap tidak men-trigger deployment otomatis.
- [ ] README menjelaskan alur PR lalu deployment manual.

---

## 16. Hasil Akhir untuk Developer

Developer hanya perlu:

1. push feature branch;
2. buka Pull Request ke `main`;
3. lihat job yang gagal dan perbaiki pada branch yang sama;
4. tunggu `All PR Checks Passed` berwarna hijau;
5. minta review dan approval;
6. merge setelah Ruleset mengizinkan;
7. bila perubahan siap dipasang ke server development, jalankan workflow
   deployment manual yang sudah tersedia.

Pemisahan ini menjaga dua keputusan tetap berbeda:

```text
PR Verification = apakah kode aman dan layak di-merge
Manual Deployment = kapan commit main dipasang ke server Besu development
```
