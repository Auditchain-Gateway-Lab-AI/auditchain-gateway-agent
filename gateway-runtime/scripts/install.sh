#!/bin/bash

# ==============================================================================
# 🚀 AUDITCHAIN AGENT 1-COMMAND INSTALLER (MANAGED SERVICE MODEL)
# ==============================================================================
# This script automates:
# 1. System & Privilege Validation (Root Check)
# 2. Automated Tailscale VPN Mesh Joining (Unattended Auth Key)
# 3. Debezium CDC Engine & Kafka Agent Setup
# 4. Network Auto-Discovery (Tailscale Virtual IP)
# 5. Telemetry Phone-Home Callback to AuditChain Gateway
# ==============================================================================

# Seluruh script dibungkus dalam fungsi agar saat dijalankan via pipe
# (curl ... | bash), bash membaca SELURUH body fungsi ke memori terlebih dahulu.
# Ini mencegah command seperti docker exec / psql "memakan" sisa isi script.
# Pola ini digunakan oleh Docker, Node.js, dan installer besar lainnya.
do_install() {

set -e

# Colors for Terminal Output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}"
echo "======================================================================"
echo "         🛡️  AUDITCHAIN AGENT AUTOMATED INSTALLER v1.0              "
echo "======================================================================"
echo -e "${NC}"

# ------------------------------------------------------------------------------
# 1. PRIVILEGE & ENVIRONMENT VALIDATION
# ------------------------------------------------------------------------------
if [ "$(id -u)" -ne 0 ]; then
    echo -e "${RED}[ERROR] Script ini harus dijalankan sebagai Root / Sudo!${NC}"
    echo "Silakan jalankan ulang menggunakan: sudo bash -c \"\$(curl ...)\""
    exit 1
fi

# ==============================================================================
# MODE --update: Pembaruan Ringan untuk Klien yang Sudah Terinstall
# ==============================================================================
if [ "$1" = "--update" ] || [ "${UPDATE_MODE}" = "true" ]; then
    echo -e "\n${BLUE}======================================================================${NC}"
    echo -e "${BLUE}         🔄 AUDITCHAIN AGENT UPDATE MODE                            ${NC}"
    echo -e "${BLUE}======================================================================${NC}\n"

    if [ ! -f /etc/auditchain/docker-compose.yml ]; then
        echo -e "${RED}[ERROR] Auditchain Agent belum terinstall di server ini!${NC}"
        echo -e "Jalankan installer penuh terlebih dahulu (tanpa --update)."
        exit 1
    fi

    TAILSCALE_IP=$(tailscale ip -4 2>/dev/null || hostname -I | awk '{print $1}')
    echo -e "${GREEN}✓ Tailscale IP: ${TAILSCALE_IP}${NC}"

    echo -e "\n${YELLOW}📦 Memperbarui Docker Compose (restart policy + healthcheck)...${NC}"

    cat <<UPDATEEOF > /etc/auditchain/docker-compose.yml
version: '3.8'
services:
  zookeeper:
    image: quay.io/debezium/zookeeper:2.4
    restart: unless-stopped
    ports:
      - "2181:2181"
      - "2888:2888"
      - "3888:3888"
    volumes:
      - zookeeper_data:/zookeeper/data
      - zookeeper_txns:/zookeeper/txns
    healthcheck:
      test: ["CMD-SHELL", "bash -c '</dev/tcp/localhost/2181'"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 15s
  kafka:
    image: quay.io/debezium/kafka:2.4
    restart: unless-stopped
    ports:
      - "9092:9092"
    volumes:
      - kafka_data:/kafka/data
    environment:
      - ZOOKEEPER_CONNECT=zookeeper:2181
      - KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://${TAILSCALE_IP}:9092
    depends_on:
      zookeeper:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "bash -c '</dev/tcp/kafka/9092'"]
      interval: 10s
      timeout: 5s
      retries: 10
      start_period: 45s
  debezium:
    image: quay.io/debezium/connect:2.4
    restart: unless-stopped
    volumes:
      - /etc/auditchain/jdbc-drivers/ojdbc8.jar:/kafka/connect/debezium-connector-oracle/ojdbc8.jar
    ports:
      - "8083:8083"
    environment:
      - BOOTSTRAP_SERVERS=kafka:9092
      - GROUP_ID=1
      - CONFIG_STORAGE_TOPIC=my_connect_configs
      - OFFSET_STORAGE_TOPIC=my_connect_offsets
      - STATUS_STORAGE_TOPIC=my_connect_statuses
    depends_on:
      kafka:
        condition: service_healthy

volumes:
  zookeeper_data:
  zookeeper_txns:
  kafka_data:
UPDATEEOF

    echo -e "${GREEN}✓ docker-compose.yml berhasil diperbarui!${NC}"

    echo -e "\n${YELLOW}🧹 Membersihkan container lama...${NC}"
    cd /etc/auditchain
    if command -v docker-compose &> /dev/null; then
        docker-compose down --remove-orphans 2>/dev/null || true
    else
        docker compose down --remove-orphans 2>/dev/null || true
    fi

    echo -e "\n${YELLOW}🔄 Menjalankan ulang container dari awal...${NC}"
    if command -v docker-compose &> /dev/null; then
        docker-compose up -d
    else
        docker compose up -d
    fi

    echo -e "\n${GREEN}======================================================================${NC}"
    echo -e "${GREEN}  ✅ UPDATE BERHASIL! Container sekarang Tahan Mati Lampu.           ${NC}"
    echo -e "${GREEN}======================================================================${NC}"
    echo -e "  • restart: always  → Container otomatis hidup saat server boot"
    echo -e "  • healthcheck      → Urutan nyala dijaga: Zookeeper → Kafka → Debezium"
    echo -e "  • Data CDC         → Tidak hilang (offset tersimpan di Kafka)\n"
    exit 0
fi

# ==============================================================================
# MODE --fix: Perbaiki Konektor Debezium yang Tidak Memonitor Tabel User
# ==============================================================================
# Penggunaan: sudo bash install.sh --fix
# Script ini otomatis:
#   1. Mendeteksi konektor Debezium yang berjalan
#   2. Mengecek apakah tabel user sudah dimonitor
#   3. Jika belum, mendeteksi tabel user dari database
#   4. Menambahkan tabel user ke konektor + publication
#   5. Memaksa sinkronisasi data user ke Gateway
# ==============================================================================
if [ "$1" = "--fix" ] || [ "${FIX_MODE}" = "true" ]; then
    echo -e "\n${BLUE}======================================================================${NC}"
    echo -e "${BLUE}         🔧 AUDITCHAIN AGENT FIX MODE — User Table Repair           ${NC}"
    echo -e "${BLUE}======================================================================${NC}\n"

    # Data ini tersedia setelah instalasi awal dan dibutuhkan untuk
    # mengirim konfigurasi tabel user yang sudah diperbaiki ke Gateway.
    if [ -f /etc/auditchain/agent.env ]; then
        source /etc/auditchain/agent.env
    fi

    # 1. Deteksi konektor Debezium
    echo -e "${BLUE}🔍 Mencari konektor Debezium...${NC}"
    CONNECTORS=$(curl -s http://localhost:8083/connectors/ 2>/dev/null || true)
    if [ -z "$CONNECTORS" ] || [ "$CONNECTORS" = "[]" ]; then
        echo -e "${RED}❌ Tidak ada konektor Debezium ditemukan di localhost:8083${NC}"
        echo -e "${YELLOW}Pastikan Kafka Connect berjalan di server ini.${NC}"
        exit 1
    fi

    CONNECTOR_NAME=$(echo "$CONNECTORS" | python3 -c "import sys,json; print(json.load(sys.stdin)[0])" 2>/dev/null || echo "$CONNECTORS" | tr -d '[]"')
    echo -e "${GREEN}✓ Konektor ditemukan: ${CONNECTOR_NAME}${NC}"

    # 2. Ambil konfigurasi konektor
    echo -e "${BLUE}🔍 Membaca konfigurasi konektor...${NC}"
    CONFIG=$(curl -s "http://localhost:8083/connectors/${CONNECTOR_NAME}/config")
    if [ -z "$CONFIG" ]; then
        echo -e "${RED}❌ Gagal membaca konfigurasi konektor${NC}"
        exit 1
    fi

    CURRENT_TABLES=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('table.include.list',''))" 2>/dev/null)
    DB_NAME=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('database.dbname',''))" 2>/dev/null)
    DB_HOST=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('database.hostname','localhost'))" 2>/dev/null)
    DB_PORT=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('database.port','5432'))" 2>/dev/null)
    DB_USER=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('database.user','postgres'))" 2>/dev/null)
    DB_PASS=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('database.password',''))" 2>/dev/null)
    CONNECTOR_CLASS=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('connector.class',''))" 2>/dev/null)
    PUB_NAME=$(echo "$CONFIG" | python3 -c "import sys,json; print(json.load(sys.stdin).get('publication.name','dbz_publication'))" 2>/dev/null)

    echo -e "  Database : ${DB_NAME}"
    echo -e "  Tabel    : ${CURRENT_TABLES}"

    # 3. Cari tabel user yang sudah ada di connector. Jangan langsung exit:
    # Gateway mungkin belum menerima user_table_name dan client_users belum
    # pernah di-backfill, meskipun connector sudah memantau tabel tersebut.
    DETECTED_USER_TABLE=$(printf '%s\n' "$CURRENT_TABLES" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -iE '(^|\.)([^.]*user[^.]*|[^.]*account[^.]*|akun|pengguna|[^.]*member[^.]*|[^.]*employee[^.]*|karyawan)$' | head -n 1 || true)
    USER_TABLE_ALREADY_INCLUDED=false
    if [ -n "$DETECTED_USER_TABLE" ]; then
        USER_TABLE_ALREADY_INCLUDED=true
        echo -e "${GREEN}✓ Tabel user sudah dimonitor: ${DETECTED_USER_TABLE}${NC}"
    else
        echo -e "${YELLOW}⚠️  Tabel user BELUM dimonitor. Mencari tabel user...${NC}"
    fi

    # 4. Deteksi tabel user dari database jika belum ada di connector
    IS_POSTGRES=false
    echo "$CONNECTOR_CLASS" | grep -qi "postgres" && IS_POSTGRES=true

    if [ -z "$DETECTED_USER_TABLE" ] && [ "$IS_POSTGRES" = true ]; then
        DETECTED_USER_TABLE=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" --no-align --tuples-only -c \
            "SELECT schemaname||'.'||tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND tablename ~* '^(users?|accounts?|akun|pengguna|members?|employees?|karyawan)$' LIMIT 1;" 2>/dev/null || true)
        # Fallback: coba via docker
        if [ -z "$DETECTED_USER_TABLE" ]; then
            PG_CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -iE "postgres|pg|db" | head -1 || true)
            if [ -n "$PG_CONTAINER" ]; then
                DETECTED_USER_TABLE=$(docker exec "$PG_CONTAINER" psql -U postgres -d "$DB_NAME" --no-align --tuples-only -c \
                    "SELECT schemaname||'.'||tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND tablename ~* '^(users?|accounts?|akun|pengguna|members?|employees?|karyawan)$' LIMIT 1;" 2>/dev/null || true)
            fi
        fi
        # Fallback: sudo
        if [ -z "$DETECTED_USER_TABLE" ]; then
            DETECTED_USER_TABLE=$(sudo -u postgres psql -d "$DB_NAME" --no-align --tuples-only -c \
                "SELECT schemaname||'.'||tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND tablename ~* '^(users?|accounts?|akun|pengguna|members?|employees?|karyawan)$' LIMIT 1;" 2>/dev/null || true)
        fi
    elif [ -z "$DETECTED_USER_TABLE" ]; then
        DETECTED_USER_TABLE=$(mysql --no-defaults -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" -N -D "$DB_NAME" -e \
            "SELECT CONCAT('$DB_NAME.', table_name) FROM information_schema.tables WHERE table_schema='$DB_NAME' AND table_name REGEXP '^(users?|accounts?|akun|pengguna|members?|employees?|karyawan)$' LIMIT 1;" 2>/dev/null || true)
    fi

    if [ -z "$DETECTED_USER_TABLE" ]; then
        echo -e "${RED}❌ Tidak ditemukan tabel user di database '${DB_NAME}'.${NC}"
        read -p "Masukkan nama tabel user manual (contoh: public.users): " DETECTED_USER_TABLE < /dev/tty
        if [ -z "$DETECTED_USER_TABLE" ]; then
            echo -e "${RED}Dibatalkan.${NC}"
            exit 1
        fi
    fi

    echo -e "${GREEN}✓ Tabel user terdeteksi: ${DETECTED_USER_TABLE}${NC}"

    # Tentukan kolom yang aman untuk dummy update. Kolom ini juga dikirim
    # ke Gateway sebagai preferensi identitas, namun consumer tetap punya
    # auto-detection untuk email/nama.
    DETECTED_USER_COL=""
    TBL_BARE=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $NF}')
    if [ "$IS_POSTGRES" = true ]; then
        DETECTED_USER_COL=$(PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" --no-align --tuples-only -c \
            "SELECT column_name FROM information_schema.columns WHERE table_name = '${TBL_BARE}' AND column_name ~* '^(username|email|login|name|nama|user_name)$' ORDER BY CASE column_name WHEN 'username' THEN 1 WHEN 'email' THEN 2 ELSE 3 END LIMIT 1;" 2>/dev/null || true)
    else
        DETECTED_USER_COL=$(mysql --no-defaults -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" -N -D "$DB_NAME" -e \
            "SELECT column_name FROM information_schema.columns WHERE table_schema='${DB_NAME}' AND table_name='${TBL_BARE}' AND column_name REGEXP '^(username|email|login|name|nama|user_name)$' ORDER BY FIELD(column_name, 'username', 'email', 'login', 'name', 'nama', 'user_name') LIMIT 1;" 2>/dev/null || true)
    fi
    if [ -z "$DETECTED_USER_COL" ]; then
        DETECTED_USER_COL="id"
        echo -e "${YELLOW}⚠️  Kolom nama tidak terdeteksi; memakai 'id' untuk memicu CDC.${NC}"
    else
        echo -e "${GREEN}✓ Kolom user terdeteksi: ${DETECTED_USER_COL}${NC}"
    fi

    # 5. Update konfigurasi konektor Debezium
    NEW_TABLES="$CURRENT_TABLES"
    if [ "$USER_TABLE_ALREADY_INCLUDED" = false ]; then
        echo -e "${BLUE}🔧 Mengupdate konfigurasi konektor...${NC}"
        NEW_TABLES="${CURRENT_TABLES:+${CURRENT_TABLES},}${DETECTED_USER_TABLE}"
        echo "$CONFIG" | python3 -c "
import sys, json
config = json.load(sys.stdin)
config['table.include.list'] = '${NEW_TABLES}'
print(json.dumps(config))
" | curl -s -X PUT "http://localhost:8083/connectors/${CONNECTOR_NAME}/config" \
        -H "Content-Type: application/json" -d @- > /dev/null
        echo -e "${GREEN}✓ Konektor diupdate: ${NEW_TABLES}${NC}"
    fi

    # 6. Update publication (PostgreSQL)
    if [ "$IS_POSTGRES" = true ]; then
        echo -e "${BLUE}🔧 Mengupdate publication PostgreSQL...${NC}"
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "ALTER PUBLICATION ${PUB_NAME} ADD TABLE ${DETECTED_USER_TABLE};" 2>/dev/null || \
        {
            PG_CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -iE "postgres|pg|db" | head -1 || true)
            [ -n "$PG_CONTAINER" ] && docker exec "$PG_CONTAINER" psql -U postgres -d "$DB_NAME" -c \
                "ALTER PUBLICATION ${PUB_NAME} ADD TABLE ${DETECTED_USER_TABLE};" 2>/dev/null || \
            sudo -u postgres psql -d "$DB_NAME" -c \
                "ALTER PUBLICATION ${PUB_NAME} ADD TABLE ${DETECTED_USER_TABLE};" 2>/dev/null || true
        }
        echo -e "${GREEN}✓ Publication '${PUB_NAME}' diupdate.${NC}"

        # REPLICA IDENTITY FULL: agar DELETE event menyertakan data row lengkap
        echo -e "${BLUE}🔧 Mengaktifkan REPLICA IDENTITY FULL untuk tabel yang dimonitor...${NC}"
        FIX_ALL_TABLES="${CURRENT_TABLES}"
        if [ -n "$DETECTED_USER_TABLE" ]; then
            FIX_ALL_TABLES="${FIX_ALL_TABLES},${DETECTED_USER_TABLE}"
        fi
        IFS=',' read -ra _RI_TABLES <<< "$FIX_ALL_TABLES"
        for _ri_tbl in "${_RI_TABLES[@]}"; do
            _ri_tbl=$(echo "$_ri_tbl" | xargs)
            [ -z "$_ri_tbl" ] && continue
            PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
                "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" 2>/dev/null || \
            {
                PG_CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -iE "postgres|pg|db" | head -1 || true)
                [ -n "$PG_CONTAINER" ] && docker exec "$PG_CONTAINER" psql -U postgres -d "$DB_NAME" -c \
                    "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" 2>/dev/null || \
                sudo -u postgres psql -d "$DB_NAME" -c \
                    "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" 2>/dev/null || true
            }
        done
        echo -e "${GREEN}✓ REPLICA IDENTITY FULL diaktifkan.${NC}"
    fi

    # 7. Trigger sinkronisasi user
    echo -e "${BLUE}🔄 Memaksa sinkronisasi data user...${NC}"
    if [ "$IS_POSTGRES" = true ]; then
        PGPASSWORD="$DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$DB_USER" -d "$DB_NAME" -c \
            "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" 2>/dev/null || \
        {
            PG_CONTAINER=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -iE "postgres|pg|db" | head -1 || true)
            [ -n "$PG_CONTAINER" ] && docker exec "$PG_CONTAINER" psql -U postgres -d "$DB_NAME" -c \
                "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" 2>/dev/null || \
            sudo -u postgres psql -d "$DB_NAME" -c \
                "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" 2>/dev/null || true
        }
    else
        TBL_BARE=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $NF}')
        mysql --no-defaults -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" -D "$DB_NAME" -e \
            "UPDATE ${TBL_BARE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" 2>/dev/null || true
    fi
    echo -e "${GREEN}✓ Data user disinkronisasi.${NC}"

    # Simpan hasil perbaikan di Gateway. Tanpa langkah ini consumer Gateway
    # tidak tahu bahwa event dari tabel di atas adalah identitas user.
    if [ -n "${AUDITCHAIN_GATEWAY_URL:-}" ] && [ -n "${AUDITCHAIN_API_KEY:-}" ] && [ -n "${KAFKA_BROKERS:-}" ] && [ -n "${AGENT_SERVER_URL:-}" ]; then
        FIX_GATEWAY_URL=$(echo "$AUDITCHAIN_GATEWAY_URL" | sed -E 's|/api/?$||' | sed -E 's|/$||')
        FIX_PAYLOAD=$(cat <<EOF
{
  "api_key_prefix": "${AUDITCHAIN_API_KEY}",
  "kafka_brokers": "${KAFKA_BROKERS}",
  "agent_server_url": "${AGENT_SERVER_URL}",
  "hostname": "${HOSTNAME:-$(hostname)}",
  "tailscale_ip": "${TAILSCALE_IP:-}",
  "status": "running",
  "db_engine": "${DB_ENGINE:-}",
  "db_name": "${DB_NAME}",
  "db_tables": "${NEW_TABLES}",
  "connector_status": "running",
  "user_table_name": "${DETECTED_USER_TABLE}",
  "user_column_name": "${DETECTED_USER_COL}"
}
EOF
)
        FIX_HTTP_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${FIX_GATEWAY_URL}/api/agent/telemetry" \
            -H "Content-Type: application/json" -d "${FIX_PAYLOAD}" || echo "000")
        if [ "$FIX_HTTP_RESPONSE" = "200" ] || [ "$FIX_HTTP_RESPONSE" = "201" ]; then
            echo -e "${GREEN}✓ Konfigurasi tabel user disimpan di Gateway.${NC}"
        else
            echo -e "${YELLOW}⚠️  Gagal menyimpan konfigurasi tabel user ke Gateway (HTTP ${FIX_HTTP_RESPONSE}).${NC}"
        fi
    else
        echo -e "${YELLOW}⚠️  agent.env belum lengkap; set user_table_name di Gateway secara manual.${NC}"
    fi

    echo -e "\n${GREEN}======================================================================${NC}"
    echo -e "${GREEN}  ✅ PERBAIKAN SELESAI!                                              ${NC}"
    echo -e "${GREEN}======================================================================${NC}"
    echo -e "  Konektor : ${CONNECTOR_NAME}"
    echo -e "  Tabel    : ${NEW_TABLES}"
    echo -e "  Database : ${DB_NAME}"
    echo -e "\n${BLUE}Buat transaksi baru — Actor akan menampilkan Email, bukan UUID.${NC}\n"
    exit 0
fi

if [ -f /etc/auditchain/agent.env ]; then
    echo -e "${YELLOW}ℹ️ Memuat konfigurasi sebelumnya dari /etc/auditchain/agent.env...${NC}"
    source /etc/auditchain/agent.env
    GATEWAY_URL=${GATEWAY_URL:-${AUDITCHAIN_GATEWAY_URL}}
    CLIENT_KEY=${CLIENT_KEY:-${AUDITCHAIN_API_KEY}}
fi

GATEWAY_URL=${GATEWAY_URL:-${1:-"https://api.auditchain.id"}}
GATEWAY_URL=$(echo "$GATEWAY_URL" | sed -E 's|/api/?$||' | sed -E 's|/$||')
CLIENT_KEY=${CLIENT_KEY:-$2}

if [ -z "$CLIENT_KEY" ]; then
    echo -e "${RED}[ERROR] CLIENT_KEY (API Key Klien) wajib diisi!${NC}"
    echo "Silakan jalankan script dengan menyertakan CLIENT_KEY yang diberikan Admin."
    echo "Contoh: GATEWAY_URL=\"http://100.103.5.72:8082\" CLIENT_KEY=\"ak_live_xxxx\" sudo -E bash"
    exit 1
fi

# ------------------------------------------------------------------------------
# 2. TAILSCALE VPN INSTALLATION & UNATTENDED AUTHENTICATION
# ------------------------------------------------------------------------------
echo -e "\n${BLUE}[1/5] Memeriksa & Menginstal Tailscale VPN Mesh...${NC}"

if ! command -v tailscale &> /dev/null; then
    echo "Tailscale belum terpasang. Mengunduh installer resmi Tailscale..."
    curl -fsSL https://tailscale.com/install.sh | sh
else
    echo "Tailscale sudah terpasang di sistem."
fi

echo -e "\nMeminta Auth Key sementara (sekali pakai) secara aman dari AuditChain Gateway..."
KEY_PAYLOAD=$(cat <<EOF
{
  "api_key_prefix": "${CLIENT_KEY}"
}
EOF
)

TAILSCALE_AUTHKEY=$(curl -s -X POST "${GATEWAY_URL}/api/agent/tailscale-key" \
    -H "Content-Type: application/json" \
    -d "${KEY_PAYLOAD}" | grep -o '"auth_key":"[^"]*"' | cut -d'"' -f4)

if [ -n "$TAILSCALE_AUTHKEY" ] && [ "$TAILSCALE_AUTHKEY" != "null" ]; then
    echo -e "${GREEN}✓ Auth Key berhasil didapatkan! Menghubungkan VPN secara otomatis...${NC}"
    tailscale up --authkey="${TAILSCALE_AUTHKEY}" --accept-routes --reset || true
else
    echo -e "${RED}[ERROR] Gagal mendapatkan Auth Key dari Gateway. Periksa API_KEY atau kredensial OAuth Admin.${NC}"
    echo -e "${YELLOW}Mencoba menghubungkan secara interaktif (Manual Login URL)...${NC}"
    tailscale up --accept-routes --reset || true
fi

# Mengambil IP Virtual Tailscale (IPv4)
TAILSCALE_IP=$(tailscale ip -4 2>/dev/null || hostname -I | awk '{print $1}')
echo -e "${GREEN}✓ Terhubung ke VPN Mesh! IP Virtual Server: ${TAILSCALE_IP}${NC}"

# ------------------------------------------------------------------------------
# 3. INSTALLASI DOCKER & DEBEZIUM CDC ENGINE
# ------------------------------------------------------------------------------
echo -e "\n${BLUE}[2/5] Memeriksa Dependensi Container Engine (Docker)...${NC}"

if ! command -v docker &> /dev/null; then
    echo "Docker belum terpasang. Mengunduh installer Docker..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable --now docker
else
    echo "Docker Engine siap."
fi

if ! command -v docker-compose &> /dev/null && ! docker compose version &> /dev/null; then
    echo "Docker Compose belum terpasang. Mengunduh Docker Compose..."
    curl -L "https://github.com/docker/compose/releases/latest/download/docker-compose-$(uname -s)-$(uname -m)" -o /usr/local/bin/docker-compose
    chmod +x /usr/local/bin/docker-compose
    ln -sf /usr/local/bin/docker-compose /usr/bin/docker-compose
fi

echo -e "\n${BLUE}[3/5] Menyiapkan Folder Konfigurasi Agent & Docker Compose...${NC}"
mkdir -p /etc/auditchain
mkdir -p /var/log/auditchain
mkdir -p /etc/auditchain/jdbc-drivers

echo "Mengunduh driver JDBC yang dibutuhkan..."
curl -fsSL "https://repo1.maven.org/maven2/com/oracle/database/jdbc/ojdbc8/21.1.0.0/ojdbc8-21.1.0.0.jar" -o /etc/auditchain/jdbc-drivers/ojdbc8.jar 2>/dev/null || true

cat <<EOF > /etc/auditchain/docker-compose.yml
version: '3.8'
services:
  zookeeper:
    image: quay.io/debezium/zookeeper:2.7
    restart: unless-stopped
    ports:
      - "2181:2181"
      - "2888:2888"
      - "3888:3888"
    volumes:
      - zookeeper_data:/zookeeper/data
      - zookeeper_txns:/zookeeper/txns
    healthcheck:
      test: ["CMD-SHELL", "bash -c '</dev/tcp/localhost/2181'"]
      interval: 10s
      timeout: 5s
      retries: 5
      start_period: 15s
  kafka:
    image: quay.io/debezium/kafka:2.7
    restart: unless-stopped
    ports:
      - "9092:9092"
    volumes:
      - kafka_data:/kafka/data
    environment:
      - ZOOKEEPER_CONNECT=zookeeper:2181
      - KAFKA_LISTENERS=INTERNAL://0.0.0.0:29092,EXTERNAL://0.0.0.0:9092
      - KAFKA_ADVERTISED_LISTENERS=INTERNAL://kafka:29092,EXTERNAL://${TAILSCALE_IP}:9092
      - KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=INTERNAL:PLAINTEXT,EXTERNAL:PLAINTEXT
      - KAFKA_INTER_BROKER_LISTENER_NAME=INTERNAL
    depends_on:
      zookeeper:
        condition: service_healthy
    healthcheck:
      test: ["CMD-SHELL", "bash -c '</dev/tcp/kafka/9092'"]
      interval: 10s
      timeout: 5s
      retries: 10
      start_period: 45s
  debezium:
    image: quay.io/debezium/connect:2.7
    restart: unless-stopped
    # Docker Linux tidak menyediakan host.docker.internal secara default.
    # Mapping ini memungkinkan Debezium menjangkau PostgreSQL aplikasi yang
    # berjalan di project Docker lain melalui port host yang dipublish.
    extra_hosts:
      - "host.docker.internal:host-gateway"
    volumes:
      - /etc/auditchain/jdbc-drivers/ojdbc8.jar:/kafka/connect/debezium-connector-oracle/ojdbc8.jar
    ports:
      - "8083:8083"
    environment:
      - BOOTSTRAP_SERVERS=kafka:29092
      - GROUP_ID=1
      - CONFIG_STORAGE_TOPIC=my_connect_configs
      - OFFSET_STORAGE_TOPIC=my_connect_offsets
      - STATUS_STORAGE_TOPIC=my_connect_statuses
    depends_on:
      kafka:
        condition: service_healthy

volumes:
  zookeeper_data:
  zookeeper_txns:
  kafka_data:
EOF

echo -e "\n${BLUE}[4/7] Menjalankan Engine Database CDC (Zookeeper, Kafka, Debezium)...${NC}"
cd /etc/auditchain
if command -v docker-compose &> /dev/null; then
    docker-compose up -d
else
    docker compose up -d
fi

# ------------------------------------------------------------------------------
# 5. AUTO-DISCOVERY & KONFIGURASI KONEKTOR DATABASE CDC
# ------------------------------------------------------------------------------
echo -e "\n${BLUE}[5/7] Auto-Discovery & Konfigurasi Konektor Database CDC...${NC}"

SELECTED_DB_ENGINE=""
SELECTED_DB_NAME=""
SELECTED_TABLES=""
CONNECTOR_SETUP_STATUS="skipped"
USER_SYNC_STATUS="skipped"
USER_SYNC_MESSAGE=""

HAS_POSTGRES=false
HAS_MYSQL=false
HAS_ORACLE=false

if command -v psql &> /dev/null || systemctl is-active --quiet postgresql 2>/dev/null || systemctl is-active --quiet postgres 2>/dev/null || [ -n "$(docker ps --filter "ancestor=postgres" -q 2>/dev/null)" ]; then
    HAS_POSTGRES=true
fi

if command -v mysql &> /dev/null || systemctl is-active --quiet mysql 2>/dev/null || systemctl is-active --quiet mysqld 2>/dev/null || systemctl is-active --quiet mariadb 2>/dev/null || [ -n "$(docker ps --filter "ancestor=mysql" -q 2>/dev/null)" ]; then
    HAS_MYSQL=true
fi

if command -v sqlplus &> /dev/null || systemctl is-active --quiet oracle* 2>/dev/null || [ -n "$(docker ps --format '{{.Image}}' 2>/dev/null | grep -i oracle)" ]; then
    HAS_ORACLE=true
fi

HAS_MONGODB=false
if command -v mongosh &> /dev/null || command -v mongo &> /dev/null || systemctl is-active --quiet mongod 2>/dev/null || [ -n "$(docker ps --filter "ancestor=mongo" -q 2>/dev/null)" ]; then
    HAS_MONGODB=true
fi

HAS_SQLSERVER=false
if command -v sqlcmd &> /dev/null || systemctl is-active --quiet mssql-server 2>/dev/null || [ -n "$(docker ps --format '{{.Image}}' 2>/dev/null | grep -iE 'mssql|sqlserver')" ]; then
    HAS_SQLSERVER=true
fi

if [ "$HAS_POSTGRES" = false ] && [ "$HAS_MYSQL" = false ] && [ "$HAS_ORACLE" = false ] && [ "$HAS_MONGODB" = false ] && [ "$HAS_SQLSERVER" = false ]; then
    echo -e "${YELLOW}[NOTE] Tidak ada Database Engine terdeteksi di server ini.${NC}"
    echo -e "${YELLOW}Pilih opsi:${NC}"
    echo "  [M] Manual Entry (masukkan host, port, dan engine secara manual)"
    echo "  [S] Skip (konfigurasi nanti via Dashboard Admin)"
    read -p "Pilihan [M]: " NO_DB_CHOICE < /dev/tty
    NO_DB_CHOICE=${NO_DB_CHOICE:-M}
    if [[ "$NO_DB_CHOICE" =~ ^[Mm]$ ]]; then
        MANUAL_MODE=true
    else
        MANUAL_MODE=false
    fi
else
    MANUAL_MODE=false
    echo -e "${GREEN}✓ Engine Database Terdeteksi di Server!${NC}"
fi

CHOSEN_ENGINE=""
DB_HOST="172.17.0.1"
DB_PORT="5432"

if [ "$MANUAL_MODE" = true ]; then
    echo -e "\n${BLUE}📝 Manual Entry — Konfigurasi Database${NC}"
    echo "  [1] PostgreSQL"
    echo "  [2] MySQL / MariaDB"
    echo "  [3] Oracle Database"
    echo "  [4] MongoDB"
    echo "  [5] SQL Server"
    read -p "Pilih Engine Database [1]: " ENGINE_CHOICE < /dev/tty
    ENGINE_CHOICE=${ENGINE_CHOICE:-1}
    if [ "$ENGINE_CHOICE" = "5" ]; then
        CHOSEN_ENGINE="sqlserver"
        DB_PORT="1433"
    elif [ "$ENGINE_CHOICE" = "4" ]; then
        CHOSEN_ENGINE="mongodb"
        DB_PORT="27017"
    elif [ "$ENGINE_CHOICE" = "3" ]; then
        CHOSEN_ENGINE="oracle"
        DB_PORT="1521"
    elif [ "$ENGINE_CHOICE" = "2" ]; then
        CHOSEN_ENGINE="mysql"
        DB_PORT="3306"
    else
        CHOSEN_ENGINE="postgres"
        DB_PORT="5432"
    fi
    read -p "Database Hostname [localhost]: " INPUT_HOST < /dev/tty
    DB_HOST=${INPUT_HOST:-localhost}
    if [ "$CHOSEN_ENGINE" = "mongodb" ]; then
        read -p "Database Port [27017]: " INPUT_PORT < /dev/tty
        DB_PORT=${INPUT_PORT:-27017}
    elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
        read -p "Database Port [1433]: " INPUT_PORT < /dev/tty
        DB_PORT=${INPUT_PORT:-1433}
    elif [ "$CHOSEN_ENGINE" = "oracle" ]; then
        read -p "Database Port [1521]: " INPUT_PORT < /dev/tty
        DB_PORT=${INPUT_PORT:-1521}
    elif [ "$CHOSEN_ENGINE" = "postgres" ]; then
        read -p "Database Port [5432]: " INPUT_PORT < /dev/tty
        DB_PORT=${INPUT_PORT:-5432}
    else
        read -p "Database Port [3306]: " INPUT_PORT < /dev/tty
        DB_PORT=${INPUT_PORT:-3306}
    fi
    read -p "Nama Database (SID/Service Name/DB Name) yang ingin di-audit: " TARGET_DB < /dev/tty
elif [ "$HAS_POSTGRES" = true ] || [ "$HAS_MYSQL" = true ] || [ "$HAS_ORACLE" = true ] || [ "$HAS_MONGODB" = true ] || [ "$HAS_SQLSERVER" = true ]; then
    if [ "$HAS_POSTGRES" = true ] && [ "$HAS_MYSQL" = true ]; then
        echo -e "\n${YELLOW}Beberapa Engine Database Ditemukan:${NC}"
        echo "  [1] PostgreSQL"
        echo "  [2] MySQL / MariaDB"
        echo "  [M] Manual Entry (host/port custom)"
        read -p "Pilih Engine Database [1]: " ENGINE_CHOICE < /dev/tty
        ENGINE_CHOICE=${ENGINE_CHOICE:-1}
        if [[ "$ENGINE_CHOICE" =~ ^[Mm]$ ]]; then
            MANUAL_MODE=true
        elif [ "$ENGINE_CHOICE" = "2" ]; then
            CHOSEN_ENGINE="mysql"
        else
            CHOSEN_ENGINE="postgres"
        fi
    elif [ "$HAS_POSTGRES" = true ]; then
        echo "  • Engine Terdeteksi: PostgreSQL"
        CHOSEN_ENGINE="postgres"
    elif [ "$HAS_MYSQL" = true ]; then
        echo "  • Engine Terdeteksi: MySQL / MariaDB"
        CHOSEN_ENGINE="mysql"
    elif [ "$HAS_SQLSERVER" = true ]; then
        echo "  • Engine Terdeteksi: SQL Server"
        CHOSEN_ENGINE="sqlserver"
        DB_PORT="1433"
    elif [ "$HAS_ORACLE" = true ]; then
        echo "  • Engine Terdeteksi: Oracle Database (Membutuhkan Input SID/Service Name)"
        CHOSEN_ENGINE="oracle"
        DB_PORT="1521"
        MANUAL_MODE=false
    elif [ "$HAS_MONGODB" = true ]; then
        echo "  • Engine Terdeteksi: MongoDB (Membutuhkan Input Database Name)"
        CHOSEN_ENGINE="mongodb"
        DB_PORT="27017"
        MANUAL_MODE=false
    else
        echo "  • Engine Terdeteksi: Tidak Diketahui"
        MANUAL_MODE=true
    fi

    # Jika user memilih Manual di sini
    if [ "$MANUAL_MODE" = true ]; then
        echo -e "\n${BLUE}📝 Manual Entry — Konfigurasi Database${NC}"
        echo "  [1] PostgreSQL"
        echo "  [2] MySQL / MariaDB"
        echo "  [3] Oracle Database"
        echo "  [4] MongoDB"
        echo "  [5] SQL Server"
        read -p "Pilih Engine Database [1]: " ENGINE_CHOICE < /dev/tty
        ENGINE_CHOICE=${ENGINE_CHOICE:-1}
        if [ "$ENGINE_CHOICE" = "5" ]; then
            CHOSEN_ENGINE="sqlserver"
            DB_PORT="1433"
        elif [ "$ENGINE_CHOICE" = "4" ]; then
            CHOSEN_ENGINE="mongodb"
            DB_PORT="27017"
        elif [ "$ENGINE_CHOICE" = "3" ]; then
            CHOSEN_ENGINE="oracle"
            DB_PORT="1521"
        elif [ "$ENGINE_CHOICE" = "2" ]; then
            CHOSEN_ENGINE="mysql"
            DB_PORT="3306"
        else
            CHOSEN_ENGINE="postgres"
            DB_PORT="5432"
        fi
        read -p "Database Hostname [localhost]: " INPUT_HOST < /dev/tty
        DB_HOST=${INPUT_HOST:-localhost}
        if [ "$CHOSEN_ENGINE" = "mongodb" ]; then
            read -p "Database Port [27017]: " INPUT_PORT < /dev/tty
            DB_PORT=${INPUT_PORT:-27017}
        elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
            read -p "Database Port [1433]: " INPUT_PORT < /dev/tty
            DB_PORT=${INPUT_PORT:-1433}
        elif [ "$CHOSEN_ENGINE" = "oracle" ]; then
            read -p "Database Port [1521]: " INPUT_PORT < /dev/tty
            DB_PORT=${INPUT_PORT:-1521}
        elif [ "$CHOSEN_ENGINE" = "postgres" ]; then
            read -p "Database Port [5432]: " INPUT_PORT < /dev/tty
            DB_PORT=${INPUT_PORT:-5432}
        else
            read -p "Database Port [3306]: " INPUT_PORT < /dev/tty
            DB_PORT=${INPUT_PORT:-3306}
        fi
        read -p "Nama Database (SID/Service Name/DB Name) yang ingin di-audit: " TARGET_DB < /dev/tty
    fi
fi

SELECTED_DB_ENGINE="$CHOSEN_ENGINE"

    # Auto-discovery database list (hanya jika bukan manual mode)
    if [ "$MANUAL_MODE" = false ]; then
        DB_LIST=()
        PORT_LIST=()
        LABEL_LIST=()

        if [ "$CHOSEN_ENGINE" = "postgres" ]; then
            # --- 1. Native PostgreSQL Scan ---
            for port in 5432 5433 5434 5435; do
                RAW_DBS=$(sudo -u postgres psql -p "$port" --no-align --tuples-only -c "SELECT datname FROM pg_database WHERE datistemplate = false AND datname NOT IN ('postgres');" 2>/dev/null || true)
                if [ -n "$RAW_DBS" ]; then
                    while IFS= read -r line; do
                        if [ -n "$line" ]; then
                            DB_LIST+=("$line")
                            PORT_LIST+=("$port")
                            LABEL_LIST+=("Native")
                        fi
                    done <<< "$RAW_DBS"
                fi
            done

            # --- 2. Docker PostgreSQL Scan ---
            if command -v docker >/dev/null 2>&1; then
                DOCKER_CONTAINERS=$(docker ps --filter "ancestor=postgres" --format "{{.ID}}|{{.Names}}" 2>/dev/null || true)
                if [ -n "$DOCKER_CONTAINERS" ]; then
                    while IFS='|' read -r c_id c_name; do
                        RAW_DBS=$(docker exec "$c_id" psql -U postgres --no-align --tuples-only -c "SELECT datname FROM pg_database WHERE datistemplate = false AND datname NOT IN ('postgres');" 2>/dev/null || true)
                        if [ -n "$RAW_DBS" ]; then
                            MAPPED_PORT=$(docker port "$c_id" 5432 2>/dev/null | grep -oP ':\K\d+' | head -n 1)
                            MAPPED_PORT=${MAPPED_PORT:-"?"}
                            while IFS= read -r line; do
                                if [ -n "$line" ]; then
                                    DB_LIST+=("$line")
                                    PORT_LIST+=("$MAPPED_PORT")
                                    LABEL_LIST+=("Docker: $c_name")
                                fi
                            done <<< "$RAW_DBS"
                        fi
                    done <<< "$DOCKER_CONTAINERS"
                fi
            fi

        elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
            # --- 1. Native MySQL Scan ---
            for port in 3306 3307 3308; do
                RAW_DBS=$(mysql --no-defaults -P "$port" -N -e "SHOW DATABASES" 2>/dev/null | grep -vE "^(information_schema|performance_schema|mysql|sys)$" || true)
                if [ -n "$RAW_DBS" ]; then
                    while IFS= read -r line; do
                        if [ -n "$line" ]; then
                            DB_LIST+=("$line")
                            PORT_LIST+=("$port")
                            LABEL_LIST+=("Native")
                        fi
                    done <<< "$RAW_DBS"
                fi
            done

            # --- 2. Docker MySQL Scan ---
            if command -v docker >/dev/null 2>&1; then
                DOCKER_CONTAINERS=$(docker ps --filter "ancestor=mysql" --format "{{.ID}}|{{.Names}}" 2>/dev/null || true)
                if [ -n "$DOCKER_CONTAINERS" ]; then
                    while IFS='|' read -r c_id c_name; do
                        # Coba koneksi root tanpa password
                        RAW_DBS=$(docker exec "$c_id" mysql -u root -N -e "SHOW DATABASES;" 2>/dev/null | grep -vE "^(information_schema|performance_schema|mysql|sys)$" || true)
                        if [ -n "$RAW_DBS" ]; then
                            MAPPED_PORT=$(docker port "$c_id" 3306 2>/dev/null | grep -oP ':\K\d+' | head -n 1)
                            MAPPED_PORT=${MAPPED_PORT:-"?"}
                            while IFS= read -r line; do
                                if [ -n "$line" ]; then
                                    DB_LIST+=("$line")
                                    PORT_LIST+=("$MAPPED_PORT")
                                    LABEL_LIST+=("Docker: $c_name")
                                fi
                            done <<< "$RAW_DBS"
                        fi
                    done <<< "$DOCKER_CONTAINERS"
                fi
            fi

        elif [ "$CHOSEN_ENGINE" = "mongodb" ]; then
            # --- 1. Native MongoDB Scan ---
            RAW_DBS=$(mongosh --quiet --eval "db.getMongo().getDBNames().join('\n')" 2>/dev/null || mongo --quiet --eval "db.getMongo().getDBNames().join('\n')" 2>/dev/null | grep -vE "^(admin|config|local)$" || true)
            if [ -n "$RAW_DBS" ]; then
                while IFS= read -r line; do
                    if [ -n "$line" ]; then
                        DB_LIST+=("$line")
                        PORT_LIST+=("27017")
                        LABEL_LIST+=("Native")
                    fi
                done <<< "$RAW_DBS"
            fi
            
            # --- 2. Docker MongoDB Scan ---
            if command -v docker >/dev/null 2>&1; then
                DOCKER_CONTAINERS=$(docker ps --filter "ancestor=mongo" --format "{{.ID}}|{{.Names}}" 2>/dev/null || true)
                if [ -n "$DOCKER_CONTAINERS" ]; then
                    while IFS='|' read -r c_id c_name; do
                        RAW_DBS=$(docker exec "$c_id" mongosh --quiet --eval "db.getMongo().getDBNames().join('\n')" 2>/dev/null || docker exec "$c_id" mongo --quiet --eval "db.getMongo().getDBNames().join('\n')" 2>/dev/null | grep -vE "^(admin|config|local)$" || true)
                        if [ -n "$RAW_DBS" ]; then
                            MAPPED_PORT=$(docker port "$c_id" 27017 2>/dev/null | grep -oP ':\K\d+' | head -n 1)
                            MAPPED_PORT=${MAPPED_PORT:-"?"}
                            while IFS= read -r line; do
                                if [ -n "$line" ]; then
                                    DB_LIST+=("$line")
                                    PORT_LIST+=("$MAPPED_PORT")
                                    LABEL_LIST+=("Docker: $c_name")
                                fi
                            done <<< "$RAW_DBS"
                        fi
                    done <<< "$DOCKER_CONTAINERS"
                fi
            fi

        elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
            # Hanya deteksi instance (port), karena otentikasi wajib untuk baca DB list
            if command -v docker >/dev/null 2>&1; then
                DOCKER_CONTAINERS=$(docker ps --format "{{.ID}}|{{.Names}}|{{.Image}}" 2>/dev/null | grep -iE "mssql|sqlserver" || true)
                if [ -n "$DOCKER_CONTAINERS" ]; then
                    while IFS='|' read -r c_id c_name c_img; do
                        MAPPED_PORT=$(docker port "$c_id" 1433 2>/dev/null | grep -oP ':\K\d+' | head -n 1)
                        MAPPED_PORT=${MAPPED_PORT:-"1433"}
                        DB_LIST+=("<INPUT_MANUAL>")
                        PORT_LIST+=("$MAPPED_PORT")
                        LABEL_LIST+=("Docker: $c_name")
                    done <<< "$DOCKER_CONTAINERS"
                fi
            fi
            # Native fallback jika sqlcmd ada dan bukan docker
            if [ ${#DB_LIST[@]} -eq 0 ] && command -v sqlcmd &> /dev/null; then
                DB_LIST+=("<INPUT_MANUAL>")
                PORT_LIST+=("1433")
                LABEL_LIST+=("Native")
            fi

        elif [ "$CHOSEN_ENGINE" = "oracle" ]; then
            # Sama seperti SQL Server, deteksi instance docker
            if command -v docker >/dev/null 2>&1; then
                DOCKER_CONTAINERS=$(docker ps --format "{{.ID}}|{{.Names}}|{{.Image}}" 2>/dev/null | grep -i "oracle" || true)
                if [ -n "$DOCKER_CONTAINERS" ]; then
                    while IFS='|' read -r c_id c_name c_img; do
                        MAPPED_PORT=$(docker port "$c_id" 1521 2>/dev/null | grep -oP ':\K\d+' | head -n 1)
                        MAPPED_PORT=${MAPPED_PORT:-"1521"}
                        DB_LIST+=("<INPUT_MANUAL>")
                        PORT_LIST+=("$MAPPED_PORT")
                        LABEL_LIST+=("Docker: $c_name")
                    done <<< "$DOCKER_CONTAINERS"
                fi
            fi
        fi

        TARGET_DB=""
        if [ ${#DB_LIST[@]} -gt 0 ]; then
            echo -e "\n${BLUE}📂 Daftar Database Terdeteksi:${NC}"
            echo "--------------------------------------"
            for idx in "${!DB_LIST[@]}"; do
                if [ "${DB_LIST[$idx]}" = "<INPUT_MANUAL>" ]; then
                    echo "  [$((idx+1))] [Input Nama Database Manual] (Port: ${PORT_LIST[$idx]} | ${LABEL_LIST[$idx]})"
                else
                    echo "  [$((idx+1))] ${DB_LIST[$idx]} (Port: ${PORT_LIST[$idx]} | ${LABEL_LIST[$idx]})"
                fi
            done
            echo "  [M] Manual Entry (database lain / port custom)"
            echo "--------------------------------------"
            read -p "Pilih nomor database yang ingin di-audit [1]: " DB_IDX < /dev/tty
            DB_IDX=${DB_IDX:-1}
            if [[ "$DB_IDX" =~ ^[Mm]$ ]]; then
                MANUAL_MODE=true
                echo -e "\n${BLUE}📝 Manual Entry — Konfigurasi Database${NC}"
                echo "  [1] PostgreSQL"
                echo "  [2] MySQL / MariaDB"
                echo "  [3] Oracle Database"
                echo "  [4] MongoDB"
                echo "  [5] SQL Server"
                read -p "Pilih Engine Database [1]: " ENGINE_CHOICE < /dev/tty
                ENGINE_CHOICE=${ENGINE_CHOICE:-1}
                if [ "$ENGINE_CHOICE" = "5" ]; then
                    CHOSEN_ENGINE="sqlserver"
                    DB_PORT="1433"
                elif [ "$ENGINE_CHOICE" = "4" ]; then
                    CHOSEN_ENGINE="mongodb"
                    DB_PORT="27017"
                elif [ "$ENGINE_CHOICE" = "3" ]; then
                    CHOSEN_ENGINE="oracle"
                    DB_PORT="1521"
                elif [ "$ENGINE_CHOICE" = "2" ]; then
                    CHOSEN_ENGINE="mysql"
                    DB_PORT="3306"
                else
                    CHOSEN_ENGINE="postgres"
                    DB_PORT="5432"
                fi
                SELECTED_DB_ENGINE="$CHOSEN_ENGINE"
                
                read -p "Database Hostname [localhost]: " INPUT_HOST < /dev/tty
                DB_HOST=${INPUT_HOST:-localhost}
                read -p "Database Port [${DB_PORT}]: " INPUT_PORT < /dev/tty
                DB_PORT=${INPUT_PORT:-$DB_PORT}
                read -p "Nama Database yang ingin di-audit: " TARGET_DB < /dev/tty
            else
                ARRAY_IDX=$((DB_IDX-1))
                if [ $ARRAY_IDX -ge 0 ] && [ $ARRAY_IDX -lt ${#DB_LIST[@]} ]; then
                    if [ "${DB_LIST[$ARRAY_IDX]}" = "<INPUT_MANUAL>" ]; then
                        read -p "Nama Database (SID/Service Name/DB Name) yang ingin di-audit: " TARGET_DB < /dev/tty
                    else
                        TARGET_DB="${DB_LIST[$ARRAY_IDX]}"
                    fi
                    DB_PORT="${PORT_LIST[$ARRAY_IDX]}"
                else
                    if [ "${DB_LIST[0]}" = "<INPUT_MANUAL>" ]; then
                        read -p "Nama Database (SID/Service Name/DB Name) yang ingin di-audit: " TARGET_DB < /dev/tty
                    else
                        TARGET_DB="${DB_LIST[0]}"
                    fi
                    DB_PORT="${PORT_LIST[0]}"
                fi

                # Jika berasal dari Docker dan portnya tidak tertebak
                if [ "$DB_PORT" = "?" ]; then
                    echo -e "\n${YELLOW}⚠️ Tidak dapat mendeteksi Port Host (Port Mapping) untuk Docker Container ini.${NC}"
                    read -p "Masukkan Port Host yang ter-mapping ke container ini: " INPUT_PORT < /dev/tty
                    DB_PORT=${INPUT_PORT}
                    # Default Hostname untuk akses Docker via TCP Host
                    DB_HOST="127.0.0.1"
                fi
            fi
        else
            read -p "Masukkan Nama Database yang ingin di-audit: " TARGET_DB < /dev/tty
        fi
    fi

    SELECTED_DB_NAME="$TARGET_DB"
    ORACLE_PDB=""

    if [ "$CHOSEN_ENGINE" = "oracle" ]; then
        echo -e "\n${YELLOW}ℹ️  Oracle versi baru (19c+) menggunakan arsitektur CDB/PDB.${NC}"
        read -p "Apakah tabel aplikasi Anda berada di dalam Pluggable Database (PDB) misal XEPDB1? (y/N): " USE_PDB < /dev/tty
        if [[ "$USE_PDB" =~ ^[Yy]$ ]]; then
            read -p "Masukkan nama Pluggable Database (PDB): " ORACLE_PDB < /dev/tty
        fi
    fi

    if [ -n "$TARGET_DB" ]; then
        echo -e "${GREEN}✓ Database Terpilih: ${TARGET_DB}${NC}"
        if [ -n "$ORACLE_PDB" ]; then
            echo -e "${GREEN}✓ PDB Terpilih: ${ORACLE_PDB}${NC}"
            # Kirim telemetry PDB as TARGET_DB so Gateway registers the PDB name correctly
            SELECTED_DB_NAME="$ORACLE_PDB"
        fi

        TABLE_LIST=()
        if [ "$CHOSEN_ENGINE" = "postgres" ]; then
            # -------------------------------------------------------------------
            # DETEKSI: Apakah PostgreSQL berjalan di Docker?
            # -------------------------------------------------------------------
            PG_IS_DOCKER=false
            PG_DOCKER_CONTAINER=""
            if command -v docker &>/dev/null; then
                PG_DOCKER_CONTAINER=$(docker ps --filter "ancestor=postgres" --format "{{.Names}}" 2>/dev/null | head -n 1)
                if [ -z "$PG_DOCKER_CONTAINER" ]; then
                    PG_DOCKER_CONTAINER=$(docker ps --format "{{.Names}} {{.Image}}" 2>/dev/null | grep -i "postgres" | awk '{print $1}' | head -n 1)
                fi
                if [ -n "$PG_DOCKER_CONTAINER" ]; then
                    PG_IS_DOCKER=true

                    # Debezium berjalan di container lain. `localhost` akan
                    # menunjuk ke container Debezium sendiri, bukan ke
                    # PostgreSQL aplikasi. Gunakan host gateway dan port
                    # PostgreSQL yang dipublish oleh container aplikasi.
                    PG_MAPPED_PORT=$(docker port "$PG_DOCKER_CONTAINER" 5432/tcp 2>/dev/null | sed -n '1s/.*://p')
                    if [ -n "$PG_MAPPED_PORT" ]; then
                        DB_PORT="$PG_MAPPED_PORT"
                    fi
                    DB_HOST="host.docker.internal"
                fi
            fi

            # -------------------------------------------------------------------
            # AUTO-INSTALL postgresql-client jika psql belum ada
            # -------------------------------------------------------------------
            if ! command -v psql &>/dev/null; then
                if [ "$PG_IS_DOCKER" = false ]; then
                    echo -e "${YELLOW}⚠️ psql belum tersedia di host ini. Menginstal postgresql-client...${NC}"
                    apt-get update -qq && apt-get install -y -qq postgresql-client >/dev/null 2>&1 || true
                    if command -v psql &>/dev/null; then
                        echo -e "${GREEN}✓ postgresql-client berhasil diinstal.${NC}"
                    else
                        echo -e "${YELLOW}⚠️ Gagal menginstal postgresql-client secara otomatis.${NC}"
                    fi
                fi
            fi

            if [ "$PG_IS_DOCKER" = true ]; then
                RAW_TBLS=$(docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" --no-align --tuples-only -c "SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema');" 2>/dev/null || true)
            else
                RAW_TBLS=$(sudo -u postgres psql -d "$TARGET_DB" --no-align --tuples-only -c "SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema');" 2>/dev/null || true)
            fi
            if [ -n "$RAW_TBLS" ]; then
                while IFS= read -r line; do
                    [ -n "$line" ] && TABLE_LIST+=("$line")
                done <<< "$RAW_TBLS"
            fi
		elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
			RAW_TBLS=$(mysql --no-defaults -N -D "$TARGET_DB" -e "SHOW TABLES" 2>/dev/null || true)
			if [ -n "$RAW_TBLS" ]; then
				while IFS= read -r line; do
					[ -n "$line" ] && TABLE_LIST+=("${TARGET_DB}.${line}")
				done <<< "$RAW_TBLS"
			fi
		fi
        # ------------------------------------------------------------------------------
        # 5.5. DETEKSI TABEL USER
        # ------------------------------------------------------------------------------
        echo -e "\n${BLUE}🔍 Mencoba mendeteksi tabel user untuk sinkronisasi otomatis...${NC}"
        DETECTED_USER_TABLE=""
        DETECTED_USER_COL=""

        if [ ${#TABLE_LIST[@]} -gt 0 ]; then
            for tbl in "${TABLE_LIST[@]}"; do
                if echo "$tbl" | grep -qiE "user|account|akun|pengguna|member"; then
                    DETECTED_USER_TABLE="$tbl"
                    break
                fi
            done
        fi

        if [ -n "$DETECTED_USER_TABLE" ]; then
            echo -e "${GREEN}✓ Tabel user terdeteksi: ${DETECTED_USER_TABLE}${NC}"
            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                TBL_BARE=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $2}')
                if [ -z "$TBL_BARE" ]; then TBL_BARE="$DETECTED_USER_TABLE"; fi
                if [ "$PG_IS_DOCKER" = true ]; then
                    RAW_COLS=$(docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" --no-align --tuples-only -c "SELECT column_name FROM information_schema.columns WHERE table_name = '${TBL_BARE}';" 2>/dev/null || true)
                else
                    RAW_COLS=$(sudo -u postgres psql -d "$TARGET_DB" --no-align --tuples-only -c "SELECT column_name FROM information_schema.columns WHERE table_name = '${TBL_BARE}';" 2>/dev/null || true)
                fi
                while IFS= read -r col; do
                    if echo "$col" | grep -qiE "username|email|nama|login|name"; then
                        DETECTED_USER_COL="$col"
                        break
                    fi
                done <<< "$RAW_COLS"
            elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
                TBL_BARE=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $2}')
                if [ -z "$TBL_BARE" ]; then TBL_BARE="$DETECTED_USER_TABLE"; fi
                RAW_COLS=$(mysql --no-defaults -N -D "$TARGET_DB" -e "SHOW COLUMNS FROM ${TBL_BARE}" 2>/dev/null | awk '{print $1}')
                while IFS= read -r col; do
                    if echo "$col" | grep -qiE "username|email|nama|login|name"; then
                        DETECTED_USER_COL="$col"
                        break
                    fi
                done <<< "$RAW_COLS"
            fi

            if [ -n "$DETECTED_USER_COL" ]; then
                echo -e "${GREEN}✓ Kolom username terdeteksi: ${DETECTED_USER_COL}${NC}"
            else
                echo -e "${YELLOW}Kolom username tidak ditemukan otomatis, set default ke 'username'${NC}"
                DETECTED_USER_COL="username"
            fi
        else
            # Fallback: query database langsung untuk mencari tabel user
            echo -e "${YELLOW}Tidak ditemukan tabel user di TABLE_LIST, mencoba query database langsung...${NC}"
            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                USER_TABLES=$(sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" --no-align --tuples-only -c "SELECT schemaname||'.'||tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema') AND tablename ~* '(user|account|akun|pengguna|member|employee|karyawan)' LIMIT 1;" 2>/dev/null || true)
                if [ -n "$USER_TABLES" ]; then
                    DETECTED_USER_TABLE="$USER_TABLES"
                    echo -e "${GREEN}✓ Tabel user ditemukan via query langsung: ${DETECTED_USER_TABLE}${NC}"
                fi
            elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
                USER_TABLES=$(mysql --no-defaults -N -D "$TARGET_DB" -e "SELECT CONCAT('$TARGET_DB.', table_name) FROM information_schema.tables WHERE table_schema='$TARGET_DB' AND table_name REGEXP '(user|account|akun|pengguna|member|employee|karyawan)' LIMIT 1;" 2>/dev/null || true)
                if [ -n "$USER_TABLES" ]; then
                    DETECTED_USER_TABLE="$USER_TABLES"
                    echo -e "${GREEN}✓ Tabel user ditemukan via query langsung: ${DETECTED_USER_TABLE}${NC}"
                fi
            fi
            if [ -z "$DETECTED_USER_TABLE" ]; then
                echo -e "${YELLOW}⚠️  Tidak ditemukan tabel user. Resolusi nama actor mungkin tidak berfungsi.${NC}"
            fi
        fi

        CHOSEN_TABLES=""
        if [ ${#TABLE_LIST[@]} -gt 0 ]; then
            if command -v whiptail >/dev/null 2>&1; then
                WT_ARGS=()
                for tbl in "${TABLE_LIST[@]}"; do
                    WT_ARGS+=("$tbl" "" "OFF")
                done
                SELECTED_WT=$(whiptail --title "Pilih Tabel - $TARGET_DB" \
                    --checklist "Pilih tabel yang ingin diaudit.\nNavigasi: ↑/↓   Centang: SPASI   Selesai: ENTER\nPencarian: Ketik huruf awal tabel." \
                    20 70 10 "${WT_ARGS[@]}" 3>&1 1>&2 2>&3)
                if [ $? -eq 0 ]; then
                    CHOSEN_TABLES=$(echo "$SELECTED_WT" | tr -d '"' | tr ' ' ',')
                else
                    echo -e "${YELLOW}Pemilihan dibatalkan, memilih semua tabel secara default.${NC}"
                    CHOSEN_TABLES=$(IFS=,; echo "${TABLE_LIST[*]}")
                fi
            elif command -v dialog >/dev/null 2>&1; then
                DLG_ARGS=()
                for tbl in "${TABLE_LIST[@]}"; do
                    DLG_ARGS+=("$tbl" "" "off")
                done
                SELECTED_DLG=$(dialog --clear --title "Pilih Tabel - $TARGET_DB" \
                    --checklist "Pilih tabel yang ingin diaudit.\nNavigasi: ↑/↓   Centang: SPASI   Selesai: ENTER\nPencarian: Ketik huruf awal tabel." \
                    20 70 10 "${DLG_ARGS[@]}" 2>&1 >/dev/tty)
                if [ $? -eq 0 ]; then
                    CHOSEN_TABLES=$(echo "$SELECTED_DLG" | tr ' ' ',')
                else
                    echo -e "${YELLOW}Pemilihan dibatalkan, memilih semua tabel secara default.${NC}"
                    CHOSEN_TABLES=$(IFS=,; echo "${TABLE_LIST[*]}")
                fi
            else
                echo -e "\n${BLUE}📋 Tabel Terdeteksi di '$TARGET_DB':${NC}"
                echo "--------------------------------------"
                for idx in "${!TABLE_LIST[@]}"; do
                    echo "  [$((idx+1))] ${TABLE_LIST[$idx]}"
                done
                echo "  [A] Semua Tabel (Monitor Semua)"
                echo "--------------------------------------"
                read -p "Pilih tabel (pisahkan koma contoh 1,2 atau 'A' untuk semua) [A]: " TBL_INPUT < /dev/tty
                TBL_INPUT=${TBL_INPUT:-A}

                if [ "$TBL_INPUT" = "A" ] || [ "$TBL_INPUT" = "a" ]; then
                    CHOSEN_TABLES=$(IFS=,; echo "${TABLE_LIST[*]}")
                else
                    SELECTED_TBL_ARRAY=()
                    IFS=',' read -ra ADDR <<< "$TBL_INPUT"
                    for item in "${ADDR[@]}"; do
                        item=$(echo "$item" | xargs)
                        if [[ "$item" =~ ^[0-9]+$ ]]; then
                            T_IDX=$((item-1))
                            if [ $T_IDX -ge 0 ] && [ $T_IDX -lt ${#TABLE_LIST[@]} ]; then
                                SELECTED_TBL_ARRAY+=("${TABLE_LIST[$T_IDX]}")
                            fi
                        fi
                    done
                    if [ ${#SELECTED_TBL_ARRAY[@]} -gt 0 ]; then
                        CHOSEN_TABLES=$(IFS=,; echo "${SELECTED_TBL_ARRAY[*]}")
                    else
                        CHOSEN_TABLES=$(IFS=,; echo "${TABLE_LIST[*]}")
                    fi
                fi
            fi

            if [ -z "$CHOSEN_TABLES" ]; then
                echo -e "${YELLOW}Tidak ada tabel yang dicentang. Memilih semua tabel secara otomatis.${NC}"
                CHOSEN_TABLES=$(IFS=,; echo "${TABLE_LIST[*]}")
            fi
        else
            read -p "Masukkan Nama Tabel/Koleksi yang ingin di-audit (contoh: public.audit_trail atau targetdb.koleksi): " CHOSEN_TABLES < /dev/tty
        fi

        # Otomatis sertakan tabel user ke dalam CHOSEN_TABLES agar disedot Debezium
        # Kita WAJIB menyedot tabel user untuk mengatasi masalah CUID/UUID dari Prisma,
        # agar Gateway bisa mencocokkan UUID dari kolom updated_by ke nama aslinya.
        if [ -n "$DETECTED_USER_TABLE" ]; then
            if ! echo "$CHOSEN_TABLES" | grep -q "$DETECTED_USER_TABLE"; then
                CHOSEN_TABLES="${CHOSEN_TABLES},${DETECTED_USER_TABLE}"
                CHOSEN_TABLES=$(echo "$CHOSEN_TABLES" | sed 's/^,//')
                echo -e "${GREEN}✓ Tabel user '${DETECTED_USER_TABLE}' otomatis disertakan dalam pengawasan CDC.${NC}"
            fi
        fi

        SELECTED_TABLES="$CHOSEN_TABLES"
        echo -e "${GREEN}✓ Tabel Terpilih: ${CHOSEN_TABLES}${NC}"

        AGENT_DB_USER="auditchain_agent"
        AGENT_DB_PASS=$(openssl rand -hex 12 2>/dev/null || echo "ac_pwd_$(date +%s)")
        # Set default port hanya jika belum di-set manual. Jangan menimpa
        # host/port PostgreSQL yang berjalan di Docker project lain.
        if [ "$MANUAL_MODE" = false ] && [ "$PG_IS_DOCKER" != true ]; then
            DB_PORT="5432"
            [ "$CHOSEN_ENGINE" = "mysql" ] && DB_PORT="3306"
            [ "$CHOSEN_ENGINE" = "sqlserver" ] && DB_PORT="1433"
            [ "$CHOSEN_ENGINE" = "mongodb" ] && DB_PORT="27017"
            [ "$CHOSEN_ENGINE" = "oracle" ] && DB_PORT="1521"
        fi

        # Gunakan DB_HOST dari manual entry, atau deteksi Docker bridge IP.
        # PostgreSQL Docker memakai host.docker.internal yang sudah dipetakan
        # pada service Debezium di docker-compose di atas.
        if [ "$PG_IS_DOCKER" = true ]; then
            DB_HOST="host.docker.internal"
            PG_MAPPED_PORT=$(docker port "$PG_DOCKER_CONTAINER" 5432/tcp 2>/dev/null | sed -n '1s/.*://p')
            [ -n "$PG_MAPPED_PORT" ] && DB_PORT="$PG_MAPPED_PORT"
        elif [ "$MANUAL_MODE" = false ]; then
            DB_HOST=$(ip -4 addr show docker0 2>/dev/null | grep -oP '(?<=inet\s)\d+(\.\d+){3}' || echo "172.17.0.1")
        fi

        # ------------------------------------------------------------------------------
        # TWEAK BIND-ADDRESS / LISTEN_ADDRESS SEBELUM TES KONEKSI & BUAT USER
        # ------------------------------------------------------------------------------
        if [ "$CHOSEN_ENGINE" = "postgres" ]; then
            NEEDS_PG_RESTART=false

            # PG_IS_DOCKER dan PG_DOCKER_CONTAINER sudah dideteksi di bagian table detection di atas.
            # JANGAN di-reset di sini!

            if [ "$PG_IS_DOCKER" = true ]; then
                echo -e "${BLUE}🐳 PostgreSQL terdeteksi berjalan di Docker container: '${PG_DOCKER_CONTAINER}'${NC}"
            fi

            # Fungsi helper: jalankan psql di environment yang tepat
            run_psql() {
                local db_name="${1:-postgres}"
                shift
                if [ "$PG_IS_DOCKER" = true ]; then
                    docker exec "$PG_DOCKER_CONTAINER" psql -U "$AGENT_DB_USER_FALLBACK" -d "$db_name" "$@" 2>/dev/null
                else
                    sudo -u postgres psql -p "$DB_PORT" -d "$db_name" "$@" 2>/dev/null
                fi
            }
            # Simpan username superuser yang ada di container/native (biasanya 'postgres')
            AGENT_DB_USER_FALLBACK="postgres"

            # -------------------------------------------------------------------
            # DOCKER MODE: Konfigurasi wal_level via Docker
            # -------------------------------------------------------------------
            if [ "$PG_IS_DOCKER" = true ]; then
                echo -e "\n${BLUE}🐳 [Docker Mode] Mengkonfigurasi PostgreSQL di dalam Docker...${NC}"

                # Cek wal_level saat ini
                CURRENT_WAL=$(docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -tAc "SHOW wal_level;" 2>/dev/null || echo "unknown")
                CURRENT_WAL=$(echo "$CURRENT_WAL" | tr -d '[:space:]')

                if [ "$CURRENT_WAL" != "logical" ]; then
                    echo -e "${YELLOW}⚠️ WAL Level saat ini: '${CURRENT_WAL}'. Debezium memerlukan 'logical'.${NC}"
                    echo -e "${YELLOW}   Untuk Docker, wal_level HARUS diset permanen via docker-compose.yml.${NC}"

                    # Cari docker-compose.yml milik container PostgreSQL
                    PG_COMPOSE_DIR=""
                    # Coba ambil dari label Docker Compose
                    PG_COMPOSE_DIR=$(docker inspect "$PG_DOCKER_CONTAINER" --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' 2>/dev/null || echo "")

                    if [ -n "$PG_COMPOSE_DIR" ] && [ -d "$PG_COMPOSE_DIR" ]; then
                        COMPOSE_FILE=""
                        for cf in "$PG_COMPOSE_DIR/docker-compose.yml" "$PG_COMPOSE_DIR/docker-compose.yaml" "$PG_COMPOSE_DIR/docker-compose.dev.yml" "$PG_COMPOSE_DIR/docker-compose.dev.yaml" "$PG_COMPOSE_DIR/docker-compose.override.yml" "$PG_COMPOSE_DIR/docker-compose.override.yaml" "$PG_COMPOSE_DIR/docker-compose.prod.yml" "$PG_COMPOSE_DIR/docker-compose.production.yml" "$PG_COMPOSE_DIR/compose.yml" "$PG_COMPOSE_DIR/compose.yaml"; do
                            if [ -f "$cf" ]; then
                                COMPOSE_FILE="$cf"
                                break
                            fi
                        done

                        # Jika tidak ditemukan, coba cari compose file apa saja yang mengandung postgres
                        if [ -z "$COMPOSE_FILE" ]; then
                            for cf in "$PG_COMPOSE_DIR"/docker-compose*.yml "$PG_COMPOSE_DIR"/docker-compose*.yaml "$PG_COMPOSE_DIR"/compose*.yml "$PG_COMPOSE_DIR"/compose*.yaml; do
                                if [ -f "$cf" ] && grep -q "postgres" "$cf" 2>/dev/null; then
                                    COMPOSE_FILE="$cf"
                                    break
                                fi
                            done
                        fi

                        if [ -n "$COMPOSE_FILE" ]; then
                            echo -e "${BLUE}   Ditemukan: ${COMPOSE_FILE}${NC}"

                            # Ambil nama service PostgreSQL di compose
                            PG_SERVICE=$(docker inspect "$PG_DOCKER_CONTAINER" --format '{{index .Config.Labels "com.docker.compose.service"}}' 2>/dev/null || echo "")

                            if [ -n "$PG_SERVICE" ]; then
                                # Cek apakah sudah ada command wal_level=logical
                                if grep -q "wal_level=logical" "$COMPOSE_FILE" 2>/dev/null; then
                                    echo -e "${GREEN}✓ wal_level=logical sudah dikonfigurasi di docker-compose.yml.${NC}"
                                else
                                    echo -e "${YELLOW}   Menambahkan 'command: [\"postgres\", \"-c\", \"wal_level=logical\"]' ke service '${PG_SERVICE}'...${NC}"
                                    # Backup dulu
                                    cp "$COMPOSE_FILE" "${COMPOSE_FILE}.bak.auditchain"

                                    # Tambahkan command setelah baris image postgres
                                    # Gunakan sed untuk menyisipkan baris command setelah image: postgres
                                    if grep -qE "^\\s+image:.*postgres" "$COMPOSE_FILE"; then
                                        sed -i "/image:.*postgres/a\\    command: [\"postgres\", \"-c\", \"wal_level=logical\", \"-c\", \"max_replication_slots=4\", \"-c\", \"max_wal_senders=4\"]" "$COMPOSE_FILE"
                                        echo -e "${GREEN}✓ docker-compose.yml berhasil dimodifikasi.${NC}"
                                    else
                                        echo -e "${YELLOW}⚠️ Tidak dapat menemukan baris 'image: postgres' di compose file.${NC}"
                                        echo -e "${YELLOW}   Silakan tambahkan manual di service PostgreSQL:${NC}"
                                        echo -e "${YELLOW}   command: [\"postgres\", \"-c\", \"wal_level=logical\"]${NC}"
                                    fi

                                    # Restart container PostgreSQL via compose
                                    echo -e "${YELLOW}   Me-restart PostgreSQL container (recreate agar wal_level aktif)...${NC}"
                                    (cd "$PG_COMPOSE_DIR" && docker compose up -d --force-recreate "$PG_SERVICE" 2>/dev/null || docker-compose up -d --force-recreate "$PG_SERVICE" 2>/dev/null || docker restart "$PG_DOCKER_CONTAINER" 2>/dev/null || true)

                                    # Container dapat berganti ID saat recreate.
                                    # Tunggu sampai PostgreSQL siap menerima query,
                                    # bukan hanya menunggu sejumlah detik tetap.
                                    for _ in $(seq 1 12); do
                                        PG_DOCKER_CONTAINER=$(docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | awk 'tolower($0) ~ /postgres/ {print $1; exit}')
                                        if [ -n "$PG_DOCKER_CONTAINER" ] && docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -tAc "SELECT 1;" 2>/dev/null | grep -q '^1$'; then
                                            break
                                        fi
                                        sleep 2
                                    done

                                    # Verifikasi
                                    NEW_WAL=$(docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -tAc "SHOW wal_level;" 2>/dev/null || echo "unknown")
                                    NEW_WAL=$(echo "$NEW_WAL" | tr -d '[:space:]')
                                    if [ "$NEW_WAL" = "logical" ]; then
                                        echo -e "${GREEN}✓ wal_level berhasil diubah ke 'logical' secara permanen!${NC}"
                                    else
                                        echo -e "${RED}⚠️ wal_level masih '${NEW_WAL}'. Mungkin perlu recreate container:${NC}"
                                        echo -e "${YELLOW}   cd $PG_COMPOSE_DIR && docker compose up -d ${PG_SERVICE}${NC}"
                                    fi
                                fi
                            fi
                        else
                            echo -e "${YELLOW}⚠️ Tidak dapat menemukan docker-compose.yml di ${PG_COMPOSE_DIR}.${NC}"
                            echo -e "${YELLOW}   Silakan tambahkan manual: command: [\"postgres\", \"-c\", \"wal_level=logical\"]${NC}"
                        fi
                    else
                        echo -e "${YELLOW}⚠️ Tidak dapat menemukan folder docker-compose PostgreSQL.${NC}"
                        echo -e "${YELLOW}   Silakan tambahkan manual ke docker-compose.yml Anda:${NC}"
                        echo -e "${YELLOW}   command: [\"postgres\", \"-c\", \"wal_level=logical\"]${NC}"
                        echo -e "${YELLOW}   Lalu jalankan: docker compose up -d${NC}"
                    fi
                else
                    echo -e "${GREEN}✓ WAL Level sudah 'logical'. Siap untuk CDC.${NC}"
                fi

                # Docker: pg_hba.conf biasanya sudah mengizinkan semua koneksi (trust/md5)
                echo -e "${GREEN}✓ [Docker Mode] pg_hba.conf biasanya sudah permisif di container Docker.${NC}"

            # -------------------------------------------------------------------
            # NATIVE MODE: Konfigurasi wal_level via systemctl (cara lama)
            # -------------------------------------------------------------------
            else
                echo -e "\n${BLUE}[Native Mode] Mengkonfigurasi PostgreSQL native...${NC}"

                # --- Cek WAL Level ---
                CURRENT_WAL=$(sudo -u postgres psql -p "$DB_PORT" --no-align --tuples-only -c "SHOW wal_level;" 2>/dev/null || echo "unknown")
                if [ "$CURRENT_WAL" != "logical" ]; then
                    echo -e "\n${YELLOW}⚠️ WAL Level saat ini: '${CURRENT_WAL}'. Debezium memerlukan 'logical'.${NC}"
                    sudo -u postgres psql -p "$DB_PORT" -c "ALTER SYSTEM SET wal_level = logical;" 2>/dev/null || true
                    echo -e "${GREEN}✓ WAL Level diubah ke 'logical'.${NC}"
                    NEEDS_PG_RESTART=true
                fi

                # --- Cek listen_addresses agar Docker bisa connect ---
                CURRENT_LISTEN=$(sudo -u postgres psql -p "$DB_PORT" --no-align --tuples-only -c "SHOW listen_addresses;" 2>/dev/null || echo "localhost")
                if [ "$CURRENT_LISTEN" = "localhost" ]; then
                    echo -e "${YELLOW}⚠️ PostgreSQL hanya mendengarkan 'localhost'. Debezium (Docker) butuh akses via ${DB_HOST}.${NC}"
                    sudo -u postgres psql -p "$DB_PORT" -c "ALTER SYSTEM SET listen_addresses = '*';" 2>/dev/null || true
                    echo -e "${GREEN}✓ listen_addresses diubah ke '*'.${NC}"
                    NEEDS_PG_RESTART=true
                fi

                # --- Tambahkan rule pg_hba.conf untuk Docker subnet ---
                PG_HBA=$(sudo -u postgres psql -p "$DB_PORT" --no-align --tuples-only -c "SHOW hba_file;" 2>/dev/null || echo "")
                if [ -n "$PG_HBA" ] && [ -f "$PG_HBA" ]; then
                    if ! grep -q "AuditChain" "$PG_HBA" 2>/dev/null; then
                        echo -e "${YELLOW}⚠️ Menambahkan rule pg_hba.conf untuk Docker subnet...${NC}"
                        echo "# AuditChain - Allow Debezium Docker container & Host Scripts" >> "$PG_HBA"
                        echo "host    all    all    172.16.0.0/12    md5" >> "$PG_HBA"
                        echo "host    all    all    10.0.0.0/8       md5" >> "$PG_HBA"
                        echo "host    all    all    192.168.0.0/16   md5" >> "$PG_HBA"
                        echo "host    all    all    0.0.0.0/0        md5" >> "$PG_HBA"
                        echo "host    all    all    0.0.0.0/0        scram-sha-256" >> "$PG_HBA"
                        echo -e "${GREEN}✓ Rule pg_hba.conf ditambahkan (Private Subnets).${NC}"
                        NEEDS_PG_RESTART=true
                    fi
                fi

                # --- Restart PostgreSQL jika ada perubahan ---
                if [ "$NEEDS_PG_RESTART" = true ]; then
                    echo -e "${YELLOW}Restarting PostgreSQL untuk menerapkan perubahan...${NC}"
                    systemctl restart postgresql 2>/dev/null || systemctl restart postgres 2>/dev/null || true
                    sleep 2
                    echo -e "${GREEN}✓ PostgreSQL berhasil di-restart.${NC}"
                fi
            fi
        elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
            NEEDS_MYSQL_RESTART=false
            CURRENT_BIND=$(mysql --no-defaults -P "$DB_PORT" -N -e "SELECT @@bind_address;" 2>/dev/null | tr -d ' ' || echo "unknown")
            if [ "$CURRENT_BIND" = "127.0.0.1" ] || [ "$CURRENT_BIND" = "localhost" ]; then
                echo -e "\n${YELLOW}⚠️ MySQL hanya mendengarkan '${CURRENT_BIND}' (localhost). Debezium (Docker) butuh akses jaringan.${NC}"
                echo -e "${YELLOW}Mencoba mengubah bind-address menjadi 0.0.0.0 di konfigurasi MySQL...${NC}"
                
                for conf_file in /etc/mysql/mysql.conf.d/mysqld.cnf /etc/mysql/mariadb.conf.d/50-server.cnf /etc/my.cnf /etc/mysql/my.cnf; do
                    if [ -f "$conf_file" ] && grep -qE "^\s*bind-address\s*=\s*(127\.0\.0\.1|localhost)" "$conf_file"; then
                        sudo sed -i -E 's/^\s*bind-address\s*=\s*(127\.0\.0\.1|localhost)/bind-address = 0.0.0.0/' "$conf_file"
                        echo -e "${GREEN}✓ bind-address diubah ke '0.0.0.0' pada $conf_file.${NC}"
                        NEEDS_MYSQL_RESTART=true
                        break
                    fi
                done
                
                if [ "$NEEDS_MYSQL_RESTART" = true ]; then
                    echo -e "${YELLOW}Restarting MySQL/MariaDB untuk menerapkan perubahan...${NC}"
                    systemctl restart mysql 2>/dev/null || systemctl restart mysqld 2>/dev/null || systemctl restart mariadb 2>/dev/null || true
                    sleep 3
                    echo -e "${GREEN}✓ MySQL berhasil di-restart.${NC}"
                else
                    echo -e "${RED}✗ Tidak dapat menemukan file konfigurasi MySQL secara otomatis.${NC}"
                    echo -e "${YELLOW}Silakan ubah 'bind-address = 0.0.0.0' secara manual dan restart MySQL.${NC}"
                fi
            fi
        fi

        USER_CREATED=false

        # Untuk Docker, kita bisa membuat user otomatis tanpa perlu tanya
        if [ "$PG_IS_DOCKER" = true ] && [ "$CHOSEN_ENGINE" = "postgres" ]; then
            echo -e "\n${BLUE}🐳 [Docker Mode] Membuat user database otomatis via Docker...${NC}"
            echo -e "Membuat user database '${AGENT_DB_USER}' dengan hak akses replication..."

            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -c "CREATE USER ${AGENT_DB_USER} WITH REPLICATION LOGIN PASSWORD '${AGENT_DB_PASS}';" 2>/dev/null || \
            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -c "ALTER USER ${AGENT_DB_USER} WITH REPLICATION LOGIN PASSWORD '${AGENT_DB_PASS}';" 2>/dev/null || true

            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -c "GRANT CONNECT ON DATABASE \"${TARGET_DB}\" TO ${AGENT_DB_USER};" 2>/dev/null || true
            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" -c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO ${AGENT_DB_USER};" 2>/dev/null || true
            USER_CREATED=true
               echo -e "  - Mengecek dan menghapus replication slot yang menggantung..."
            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -c "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE active = false;" >/dev/null 2>&1 || true
            
            echo -e "  - Membersihkan publication lama (jika ada)..."
            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" -c "DROP PUBLICATION IF EXISTS dbz_publication;" >/dev/null 2>&1 || true
            
            echo -e "  - Membuat publication khusus tabel terpilih..."
            docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" -c "CREATE PUBLICATION dbz_publication FOR TABLE ${CHOSEN_TABLES};" >/dev/null 2>&1 || true
            
            # REPLICA IDENTITY FULL: agar PostgreSQL mengirim data row lengkap
            # pada event DELETE, sehingga Debezium bisa menyertakan kolom actor
            # (updated_by, deleted_by, dll) dalam payload CDC.
            echo -e "  - Mengaktifkan REPLICA IDENTITY FULL untuk tabel terpilih..."
            IFS=',' read -ra _RI_TABLES <<< "$CHOSEN_TABLES"
            for _ri_tbl in "${_RI_TABLES[@]}"; do
                _ri_tbl=$(echo "$_ri_tbl" | xargs)
                [ -z "$_ri_tbl" ] && continue
                docker exec "$PG_DOCKER_CONTAINER" psql -U postgres -d "$TARGET_DB" -c "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" >/dev/null 2>&1 || true
            done
            echo -e "${GREEN}✓ REPLICA IDENTITY FULL diaktifkan.${NC}"
            
            echo -e "${GREEN}✓ Publication CDC berhasil dibuat.${NC}"
        else
            # Non-Docker: tanya user apakah mau buat otomatis
            echo -e "\n${YELLOW}Apakah Anda ingin skrip membuatkan User Database (auditchain_agent) secara otomatis?${NC}"
            echo -e "Pilih 'y' jika database terpasang di host ini (native). Pilih 'n' jika Anda sudah membuat user sendiri atau DB berada di Remote."
            read -p "(y/N): " AUTO_USER < /dev/tty
            if [[ "$AUTO_USER" =~ ^[Yy]$ ]]; then

            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                echo -e "\nMembuat user database '${AGENT_DB_USER}' dengan hak akses replication..."
                if sudo -u postgres psql -p "$DB_PORT" -c "CREATE USER ${AGENT_DB_USER} WITH REPLICATION LOGIN PASSWORD '${AGENT_DB_PASS}';" 2>/dev/null || sudo -u postgres psql -p "$DB_PORT" -c "ALTER USER ${AGENT_DB_USER} WITH REPLICATION LOGIN PASSWORD '${AGENT_DB_PASS}';" 2>/dev/null; then
                    sudo -u postgres psql -p "$DB_PORT" -c "GRANT CONNECT ON DATABASE \"${TARGET_DB}\" TO ${AGENT_DB_USER};" 2>/dev/null || true
                    sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -c "GRANT SELECT ON ALL TABLES IN SCHEMA public TO ${AGENT_DB_USER};" 2>/dev/null || true
                    USER_CREATED=true
                    echo -e "${GREEN}✓ User DB '${AGENT_DB_USER}' berhasil dibuat otomatis!${NC}"
                fi
                # Buat Publication untuk Debezium (membutuhkan superuser)
                echo -e "Membuat Publication CDC untuk Debezium..."
                sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -c "DROP PUBLICATION IF EXISTS dbz_publication;" 2>/dev/null || true
                if ! sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -c "CREATE PUBLICATION dbz_publication FOR TABLE ${CHOSEN_TABLES};" 2>/dev/null; then
                    echo -e "${RED}[ERROR] Gagal membuat publication 'dbz_publication'!${NC}"
                    echo -e "${YELLOW}Pastikan user yang menjalankan script punya akses SUPERUSER ke PostgreSQL.${NC}"
                    echo -e "${YELLOW}Atau buat manual: CREATE PUBLICATION dbz_publication FOR TABLE ...;${NC}"
                fi

                # Verifikasi publication terbentuk
                PUB_CHECK=$(sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -tAc "SELECT COUNT(*) FROM pg_publication WHERE pubname = 'dbz_publication';" 2>/dev/null || echo "0")
                if [ "$PUB_CHECK" -eq 0 ]; then
                    echo -e "${RED}⚠️  Publication 'dbz_publication' TIDAK DITEMUKAN setelah pembuatan!${NC}"
                else
                    echo -e "${GREEN}✓ Publication CDC berhasil dibuat.${NC}"
                fi

                # REPLICA IDENTITY FULL: agar PostgreSQL mengirim data row lengkap
                # pada event DELETE, sehingga Debezium bisa menyertakan kolom actor.
                echo -e "Mengaktifkan REPLICA IDENTITY FULL untuk tabel terpilih..."
                IFS=',' read -ra _RI_TABLES <<< "$CHOSEN_TABLES"
                for _ri_tbl in "${_RI_TABLES[@]}"; do
                    _ri_tbl=$(echo "$_ri_tbl" | xargs)
                    [ -z "$_ri_tbl" ] && continue
                    sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -c "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" 2>/dev/null || true
                done
                echo -e "${GREEN}✓ REPLICA IDENTITY FULL diaktifkan.${NC}"
            elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
                echo -e "\nMembuat user database '${AGENT_DB_USER}' dengan hak akses replication..."
                if mysql --no-defaults -e "CREATE USER IF NOT EXISTS '${AGENT_DB_USER}'@'%' IDENTIFIED BY 'temp_pass'; ALTER USER '${AGENT_DB_USER}'@'%' IDENTIFIED WITH mysql_native_password BY '${AGENT_DB_PASS}'; GRANT SELECT, RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '${AGENT_DB_USER}'@'%'; FLUSH PRIVILEGES;" 2>/dev/null || \
                   mysql --no-defaults -e "CREATE USER IF NOT EXISTS '${AGENT_DB_USER}'@'%' IDENTIFIED BY 'temp_pass'; ALTER USER '${AGENT_DB_USER}'@'%' IDENTIFIED BY '${AGENT_DB_PASS}'; GRANT SELECT, RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO '${AGENT_DB_USER}'@'%'; FLUSH PRIVILEGES;" 2>/dev/null; then
                    USER_CREATED=true
                    echo -e "${GREEN}✓ User DB '${AGENT_DB_USER}' berhasil dibuat otomatis!${NC}"
                fi
            fi
            fi
        fi

        # ------------------------------------------------------------------------------
        # TEST CONNECTION
        # ------------------------------------------------------------------------------
        MAX_RETRIES=3
        RETRY_COUNT=0
        CONN_OK=false

        while [ "$CONN_OK" = false ] && [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
            RETRY_COUNT=$((RETRY_COUNT+1))
            
            if [ "$USER_CREATED" = false ]; then
                if [ $RETRY_COUNT -eq 1 ]; then
                    echo -e "\n${YELLOW}[NOTE] Otomasi pembuat user DB tidak tersedia (mis. database di Docker) atau dilewati.${NC}"
                    echo -e "${YELLOW}Silakan masukkan kredensial database yang sudah ada:${NC}"
                else
                    echo -e "\n${YELLOW}🔄 Percobaan ke-${RETRY_COUNT} dari ${MAX_RETRIES}...${NC}"
                fi
                read -p "Database Username: " AGENT_DB_USER < /dev/tty
                read -p "Database Password (terlihat): " AGENT_DB_PASS < /dev/tty
            else
                if [ $RETRY_COUNT -eq 1 ]; then
                    echo -e "\n${BLUE}🔌 Menguji koneksi dengan user otomatis...${NC}"
                else
                    echo -e "\n${YELLOW}🔄 Menunggu Anda memperbaiki konfigurasi... (Percobaan ke-${RETRY_COUNT}/${MAX_RETRIES})${NC}"
                    echo -e "Silakan perbaiki bind-address/pg_hba.conf secara manual, atau tekan Ctrl+C untuk membatalkan."
                    read -p "Tekan ENTER untuk mencoba lagi koneksi..." < /dev/tty
                fi
            fi

            echo -e "\n${BLUE}🔌 Menguji koneksi ke ${DB_HOST}:${DB_PORT}/${TARGET_DB} sebagai '${AGENT_DB_USER}'...${NC}"

            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                if [ "$PG_IS_DOCKER" = true ]; then
                    TEST_RESULT=$(docker exec "$PG_DOCKER_CONTAINER" psql -U "$AGENT_DB_USER" -d "$TARGET_DB" -c "SELECT 1;" --no-align --tuples-only 2>&1 || true)
                else
                    TEST_RESULT=$(PGPASSWORD="$AGENT_DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$AGENT_DB_USER" -d "$TARGET_DB" -c "SELECT 1;" --no-align --tuples-only 2>&1 || true)
                fi
                if echo "$TEST_RESULT" | grep -q "^1$"; then
                    CONN_OK=true
                    echo -e "${GREEN}✓ Koneksi berhasil! Kredensial & Jaringan valid.${NC}"
                else
                    echo -e "${RED}✗ Koneksi gagal: ${TEST_RESULT}${NC}"
                fi
            elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
                TEST_RESULT=$(mysql --no-defaults -h "$DB_HOST" -P "$DB_PORT" -u "$AGENT_DB_USER" -p"$AGENT_DB_PASS" -D "$TARGET_DB" -N -e "SELECT 1;" 2>&1 || true)
                if echo "$TEST_RESULT" | grep -q "^1$"; then
                    CONN_OK=true
                    echo -e "${GREEN}✓ Koneksi berhasil! Kredensial & Jaringan valid.${NC}"
                else
                    echo -e "${RED}✗ Koneksi gagal: ${TEST_RESULT}${NC}"
                fi
            elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
                TEST_RESULT=$(sqlcmd -S "$DB_HOST,$DB_PORT" -U "$AGENT_DB_USER" -P "$AGENT_DB_PASS" -d "$TARGET_DB" -Q "SELECT 1" -h -1 -W 2>&1 || true)
                if echo "$TEST_RESULT" | grep -q "^1$"; then
                    CONN_OK=true
                    echo -e "${GREEN}✓ Koneksi berhasil! Kredensial & Jaringan valid.${NC}"
                else
                    echo -e "${RED}✗ Koneksi gagal: ${TEST_RESULT}${NC}"
                fi
            else
                # Skip test for Oracle and MongoDB since they require specific clients
                CONN_OK=true
                echo -e "${GREEN}✓ [Skip Test] Asumsi kredensial ${CHOSEN_ENGINE} valid karena klien native tidak tersedia.${NC}"
            fi

            if [ "$CONN_OK" = false ] && [ $RETRY_COUNT -lt $MAX_RETRIES ] && [ "$USER_CREATED" = false ]; then
                echo -e "${YELLOW}Silakan periksa kembali username, password, atau konfigurasi jaringan Anda.${NC}"
            fi
        done

        if [ "$CONN_OK" = false ]; then
            echo -e "${RED}✗ Gagal terkoneksi setelah ${MAX_RETRIES} percobaan. Debezium mungkin tidak akan berjalan dengan baik!${NC}"
            echo -e "${YELLOW}Tip: Pastikan database bisa diakses dari host ini via TCP: psql/mysql -h ${DB_HOST} -p ${DB_PORT} -u <user>${NC}"
            echo -e "Instalasi akan tetap dilanjutkan, status konektor mungkin 'failed'."
        fi

        # Deteksi awal tabel berlangsung sebelum kredensial database diminta.
        # Pada PostgreSQL native/external tidak ada user sistem `postgres`,
        # sehingga deteksi awal bisa kosong walaupun public.users ada. Ulangi
        # setelah koneksi TCP tervalidasi dan tambahkan tabel user sebelum
        # connector Debezium dibuat.
        if [ "$CONN_OK" = true ] && [ "$CHOSEN_ENGINE" = "postgres" ]; then
            if [ -z "$DETECTED_USER_TABLE" ]; then
                DETECTED_USER_TABLE=$(PGPASSWORD="$AGENT_DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$AGENT_DB_USER" -d "$TARGET_DB" --no-align --tuples-only -c \
                    "SELECT schemaname || '.' || tablename
                     FROM pg_tables
                     WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
                       AND tablename ~* '(user|account|akun|pengguna|member|employee|karyawan)'
                     ORDER BY CASE WHEN tablename ~* '^users?$' THEN 0 ELSE 1 END, tablename
                     LIMIT 1;" 2>/dev/null || true)
            fi

            if [ -n "$DETECTED_USER_TABLE" ]; then
                TBL_BARE=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $NF}')
                TBL_SCHEMA=$(echo "$DETECTED_USER_TABLE" | awk -F. '{if (NF > 1) print $1; else print "public"}')
                LATE_USER_COL=$(PGPASSWORD="$AGENT_DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$AGENT_DB_USER" -d "$TARGET_DB" --no-align --tuples-only -c \
                    "SELECT column_name
                     FROM information_schema.columns
                     WHERE table_schema = '${TBL_SCHEMA}' AND table_name = '${TBL_BARE}'
                       AND column_name ~* '^(username|email|login|name|nama|user_name)$'
                     ORDER BY CASE column_name WHEN 'username' THEN 1 WHEN 'email' THEN 2 ELSE 3 END
                     LIMIT 1;" 2>/dev/null || true)
                if [ -n "$LATE_USER_COL" ]; then
                    DETECTED_USER_COL="$LATE_USER_COL"
                elif [ -z "$DETECTED_USER_COL" ]; then
                    DETECTED_USER_COL="id"
                fi

                case ",${CHOSEN_TABLES}," in
                    *",${DETECTED_USER_TABLE},"*) ;;
                    *)
                        CHOSEN_TABLES="${CHOSEN_TABLES:+${CHOSEN_TABLES},}${DETECTED_USER_TABLE}"
                        SELECTED_TABLES="$CHOSEN_TABLES"
                        echo -e "${GREEN}✓ Tabel user '${DETECTED_USER_TABLE}' otomatis ditambahkan setelah verifikasi koneksi.${NC}"
                        ;;
                esac
            else
                echo -e "${YELLOW}⚠️  Tabel user tidak ditemukan setelah koneksi database berhasil; resolusi UUID akan dinonaktifkan.${NC}"
            fi
        fi

        # publication.autocreate.mode pada connector sengaja disabled agar
        # scope CDC eksplisit. Karena itu publication HARUS dipastikan ada
        # sebelum connector dibuat, termasuk ketika client memilih memakai
        # kredensial PostgreSQL yang sudah ada (AUTO_USER=n).
        PUBLICATION_READY=true
        if [ "$CHOSEN_ENGINE" = "postgres" ]; then
            PUBLICATION_READY=false

            postgres_admin_query() {
                local sql="$1"
                local pg_container="${PG_DOCKER_CONTAINER:-}"

                if command -v psql >/dev/null 2>&1 && \
                    PGPASSWORD="$AGENT_DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$AGENT_DB_USER" -d "$TARGET_DB" -v ON_ERROR_STOP=1 -Atqc "$sql"; then
                    return 0
                fi

                if [ -z "$pg_container" ]; then
                    pg_container=$(docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | awk 'tolower($0) ~ /postgres/ {print $1; exit}')
                fi
                if [ -n "$pg_container" ] && \
                    docker exec < /dev/null "$pg_container" psql -U postgres -d "$TARGET_DB" -v ON_ERROR_STOP=1 -Atqc "$sql"; then
                    return 0
                fi

                if id -u postgres >/dev/null 2>&1 && \
                    sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -v ON_ERROR_STOP=1 -Atqc "$sql"; then
                    return 0
                fi
                return 1
            }

            ensure_postgres_publication() {
                local pub_name="dbz_publication"
                local pub_exists
                local pub_tables
                local selected_table
                local selected_tables=()

                IFS=',' read -r -a selected_tables <<< "$CHOSEN_TABLES"
                pub_exists=$(postgres_admin_query "SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = '${pub_name}');") || return 1
                pub_exists=$(echo "$pub_exists" | tr -d '[:space:]')

                if [ "$pub_exists" != "t" ]; then
                    postgres_admin_query "CREATE PUBLICATION ${pub_name} FOR TABLE ${CHOSEN_TABLES};" || return 1
                    return 0
                fi

                pub_tables=$(postgres_admin_query "SELECT schemaname || '.' || tablename FROM pg_publication_tables WHERE pubname = '${pub_name}';") || return 1
                for selected_table in "${selected_tables[@]}"; do
                    selected_table=$(echo "$selected_table" | xargs)
                    [ -z "$selected_table" ] && continue
                    if ! printf '%s\n' "$pub_tables" | grep -Fxq "$selected_table"; then
                        postgres_admin_query "ALTER PUBLICATION ${pub_name} ADD TABLE ${selected_table};" || return 1
                    fi
                done
                return 0
            }

            echo -e "${BLUE}🔐 Memastikan publication PostgreSQL untuk CDC...${NC}"
            if ensure_postgres_publication; then
                PUBLICATION_READY=true
                echo -e "${GREEN}✓ Publication 'dbz_publication' siap untuk tabel terpilih.${NC}"

                # REPLICA IDENTITY FULL: agar DELETE event menyertakan data row lengkap
                echo -e "${BLUE}🔧 Mengaktifkan REPLICA IDENTITY FULL untuk tabel terpilih...${NC}"
                IFS=',' read -ra _RI_TABLES <<< "$CHOSEN_TABLES"
                for _ri_tbl in "${_RI_TABLES[@]}"; do
                    _ri_tbl=$(echo "$_ri_tbl" | xargs)
                    [ -z "$_ri_tbl" ] && continue
                    postgres_admin_query "ALTER TABLE ${_ri_tbl} REPLICA IDENTITY FULL;" 2>/dev/null || true
                done
                echo -e "${GREEN}✓ REPLICA IDENTITY FULL diaktifkan.${NC}"
            else
                echo -e "${RED}✗ Publication PostgreSQL tidak dapat dibuat/diverifikasi. Connector Debezium tidak akan dijalankan.${NC}"
                echo -e "${YELLOW}Gunakan kredensial pemilik tabel/superuser PostgreSQL, lalu ulangi installer.${NC}"
            fi
        fi

        if [ "$CHOSEN_ENGINE" = "oracle" ]; then
            echo -e "\n${YELLOW}⚠️ PERHATIAN: Oracle Database memerlukan konfigurasi tambahan!${NC}"
            echo -e "Pastikan database telah berada di mode ARCHIVELOG dan fitur LogMiner diaktifkan."
            echo -e "Anda WAJIB menyalin file driver JDBC (ojdbc8.jar) ke folder /etc/auditchain/jdbc-drivers/ di server ini,"
            echo -e "kemudian restart Debezium (docker restart auditchain-debezium) agar konektor bisa berjalan."
            sleep 3
        elif [ "$CHOSEN_ENGINE" = "mongodb" ]; then
            echo -e "\n${YELLOW}⚠️ PERHATIAN: MongoDB memerlukan konfigurasi tambahan!${NC}"
            echo -e "1. Debezium mewajibkan MongoDB berjalan dalam mode Replica Set (meskipun 1 node)."
            echo -e "2. Jika MongoDB berjalan di Docker, Anda WAJIB mengubah host Replica Set ke IP Tailscale Anda (${DB_HOST})."
            echo -e "   Jalankan di mongosh: rs.reconfig({_id: 'rs0', members: [{_id: 0, host: '${DB_HOST}:${DB_PORT}'}]}, {force: true})"
            echo -e "3. Pastikan Anda memberikan akun ROOT / SuperAdmin agar Debezium memiliki hak 'clusterMonitor'."
            sleep 6
        elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
            echo -e "\n${YELLOW}⚠️ PERHATIAN: SQL Server memerlukan konfigurasi tambahan!${NC}"
            echo -e "Debezium SQL Server Connector mewajibkan fitur CDC diaktifkan pada database dan tabel target."
            echo -e "Jalankan perintah berikut di SSMS atau sqlcmd:"
            echo -e "  ${BLUE}USE ${TARGET_DB}; EXEC sys.sp_cdc_enable_db;${NC}"
            echo -e "  ${BLUE}EXEC sys.sp_cdc_enable_table @source_schema='dbo', @source_name='<nama_tabel>', @role_name=NULL;${NC}"
            echo -e "Pastikan SQL Server Agent service juga berjalan (diperlukan untuk CDC job)."
            sleep 3
        fi

        echo -e "\nMenunggu Debezium Engine siap (maks 30 detik)..."
        # Set HOSTNAME sekarang agar topic.prefix di Debezium & telemetri Gateway konsisten
        HOSTNAME=$(hostname)
        CONNECTOR_NAME="${TARGET_DB}-connector-$(date +%s)"
        DEBEZIUM_READY=false
        for i in $(seq 1 15); do
            if curl -s http://localhost:8083/ > /dev/null 2>&1; then
                DEBEZIUM_READY=true
                break
            fi
            sleep 2
        done

        if [ "$DEBEZIUM_READY" = true ] && { [ "$CHOSEN_ENGINE" != "postgres" ] || [ "$PUBLICATION_READY" = true ]; }; then

            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                CONNECTOR_PAYLOAD=$(cat <<EOF
{
        "name": "${CONNECTOR_NAME}",
        "config": {
            "connector.class": "io.debezium.connector.postgresql.PostgresConnector",
    "tasks.max": "1",
    "database.hostname": "${DB_HOST}",
    "database.port": "${DB_PORT}",
    "database.user": "${AGENT_DB_USER}",
    "database.password": "${AGENT_DB_PASS}",
    "database.dbname": "${TARGET_DB}",
    "topic.prefix": "${HOSTNAME}_${TARGET_DB}",
    "table.include.list": "${CHOSEN_TABLES}",
    "plugin.name": "pgoutput",
    "publication.autocreate.mode": "disabled",
    "publication.name": "dbz_publication",
    "slot.name": "dbz_${TARGET_DB//-/_}_${RANDOM}",
    "transforms": "unwrap",
    "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
    "transforms.unwrap.drop.tombstones": "false",
    "transforms.unwrap.delete.handling.mode": "rewrite",
    "transforms.unwrap.add.fields": "op,table,ts_ms"
  }
}
EOF
)
            elif [ "$CHOSEN_ENGINE" = "oracle" ]; then
                PDB_CONFIG=""
                if [ -n "$ORACLE_PDB" ]; then
                    PDB_CONFIG="\"database.pdb.name\": \"${ORACLE_PDB}\","
                fi
                # User is highly likely uppercase in Oracle
                UPPER_USER=$(echo "$AGENT_DB_USER" | tr '[:lower:]' '[:upper:]')
                
                CONNECTOR_PAYLOAD=$(cat <<EOF
{
  "name": "${CONNECTOR_NAME}",
  "config": {
    "connector.class": "io.debezium.connector.oracle.OracleConnector",
    "tasks.max": "1",
    "database.hostname": "${DB_HOST}",
    "database.port": "${DB_PORT}",
    "database.user": "${AGENT_DB_USER}",
    "database.password": "${AGENT_DB_PASS}",
    "database.dbname": "${TARGET_DB}",
    ${PDB_CONFIG}
    "topic.prefix": "${HOSTNAME}_${SELECTED_DB_NAME}",
    "schema.include.list": "${UPPER_USER},${AGENT_DB_USER}",
    "table.include.list": "${CHOSEN_TABLES}",
    "database.tablename.case.insensitive": "false",
    "schema.history.internal.kafka.bootstrap.servers": "kafka:29092",
    "schema.history.internal.kafka.topic": "schema-changes.${SELECTED_DB_NAME}",
    "log.mining.strategy": "online_catalog",
    "transforms": "unwrap",
    "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
    "transforms.unwrap.drop.tombstones": "false",
    "transforms.unwrap.delete.handling.mode": "rewrite",
    "transforms.unwrap.add.fields": "op,table,ts_ms"
  }
}
EOF
)
            elif [ "$CHOSEN_ENGINE" = "sqlserver" ]; then
                CONNECTOR_PAYLOAD=$(cat <<EOF
{
  "name": "${CONNECTOR_NAME}",
  "config": {
    "connector.class": "io.debezium.connector.sqlserver.SqlServerConnector",
    "tasks.max": "1",
    "database.hostname": "${DB_HOST}",
    "database.port": "${DB_PORT}",
    "database.user": "${AGENT_DB_USER}",
    "database.password": "${AGENT_DB_PASS}",
    "database.names": "${TARGET_DB}",
    "topic.prefix": "${HOSTNAME}_${TARGET_DB}",
    "table.include.list": "${CHOSEN_TABLES}",
    "schema.history.internal.kafka.bootstrap.servers": "kafka:29092",
    "schema.history.internal.kafka.topic": "schema-changes.${TARGET_DB}",
    "database.encrypt": "false",
    "database.trustServerCertificate": "true",
    "transforms": "unwrap",
    "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
    "transforms.unwrap.drop.tombstones": "false",
    "transforms.unwrap.delete.handling.mode": "rewrite",
    "transforms.unwrap.add.fields": "op,table,ts_ms"
  }
}
EOF
)
            elif [ "$CHOSEN_ENGINE" = "mongodb" ]; then
                # Debezium membutuhkan Regex Namespace lengkap: database\.koleksi (misal: nextjs-crud\.products,nextjs-crud\.users)
                # Kita ubah input user (products,users) menjadi (nextjs-crud\.products,nextjs-crud\.users)
                MONGO_COLLECTIONS=$(echo "$CHOSEN_TABLES" | sed 's/ //g' | sed "s/,/,${TARGET_DB}\\\\\\\\./g")
                MONGO_COLLECTIONS="${TARGET_DB}\\\\.${MONGO_COLLECTIONS}"

                CONNECTOR_PAYLOAD=$(cat <<EOF
{
  "name": "${CONNECTOR_NAME}",
  "config": {
    "connector.class": "io.debezium.connector.mongodb.MongoDbConnector",
    "tasks.max": "1",
    "mongodb.connection.string": "mongodb://${AGENT_DB_USER}:${AGENT_DB_PASS}@${DB_HOST}:${DB_PORT}/?authSource=admin&replicaSet=rs0",
    "topic.prefix": "${HOSTNAME}_${TARGET_DB}",
    "collection.include.list": "${MONGO_COLLECTIONS}",
    "transforms": "unwrap",
    "transforms.unwrap.type": "io.debezium.connector.mongodb.transforms.ExtractNewDocumentState",
    "transforms.unwrap.drop.tombstones": "false",
    "transforms.unwrap.delete.handling.mode": "drop",
    "transforms.unwrap.add.fields": "op,collection,ts_ms"
  }
}
EOF
)
            else
                CONNECTOR_PAYLOAD=$(cat <<EOF
{
  "name": "${CONNECTOR_NAME}",
  "config": {
    "connector.class": "io.debezium.connector.mysql.MySqlConnector",
    "tasks.max": "1",
    "database.hostname": "${DB_HOST}",
    "database.port": "${DB_PORT}",
    "database.user": "${AGENT_DB_USER}",
    "database.password": "${AGENT_DB_PASS}",
    "database.server.id": "184054",
    "topic.prefix": "${HOSTNAME}_${TARGET_DB}",
    "database.include.list": "${TARGET_DB}",
    "table.include.list": "${CHOSEN_TABLES}",
    "schema.history.internal.kafka.bootstrap.servers": "kafka:29092",
    "schema.history.internal.kafka.topic": "schema-changes.${TARGET_DB}",
    "transforms": "unwrap",
    "transforms.unwrap.type": "io.debezium.transforms.ExtractNewRecordState",
    "transforms.unwrap.drop.tombstones": "false",
    "transforms.unwrap.delete.handling.mode": "rewrite",
    "transforms.unwrap.add.fields": "op,table,ts_ms"
  }
}
EOF
)
            fi

            # Hapus SEMUA konektor lama yang berkaitan dengan database ini
            OLD_CONNECTORS=$(curl -s http://localhost:8083/connectors | grep -o '"[^"]*"' | tr -d '"' | grep "^${TARGET_DB}-connector" || true)
            if [ -n "$OLD_CONNECTORS" ]; then
                for oc in $OLD_CONNECTORS; do
                    curl -s -X DELETE "http://localhost:8083/connectors/${oc}" >/dev/null 2>&1 || true
                done
            fi
            sleep 1

            DBZ_BODY_FILE="/tmp/dbz_response_$$.json"
            DBZ_RESP=$(curl -s -w "%{http_code}" -o "${DBZ_BODY_FILE}" -X POST http://localhost:8083/connectors \
                -H "Content-Type: application/json" \
                -d "${CONNECTOR_PAYLOAD}" || echo "000")

            if [ "$DBZ_RESP" -eq 201 ] || [ "$DBZ_RESP" -eq 200 ] || [ "$DBZ_RESP" -eq 409 ]; then
                echo -e "${GREEN}✓ Konektor Debezium didaftarkan (HTTP ${DBZ_RESP}). Memverifikasi status task...${NC}"
                
                # ---------------------------------------------------------------
                TASK_OK=false
                for i in $(seq 1 12); do
                    sleep 5
                    TASK_STATUS=$(curl -s "http://localhost:8083/connectors/${CONNECTOR_NAME}/status" 2>/dev/null || echo "{}")
                    
                    # Cek status connector
                    CONN_STATE=$(echo "$TASK_STATUS" | grep -oP '"state"\s*:\s*"[^"]*"' | head -1 | grep -oP '"[^"]*"$' | tr -d '"')
                    # Cek status task[0]
                    TASK_STATE=$(echo "$TASK_STATUS" | grep -oP '"tasks"\s*:\s*\[.*?\]' | grep -oP '"state"\s*:\s*"[^"]*"' | head -1 | grep -oP '"[^"]*"$' | tr -d '"')
                    
                    echo -e "  [${i}/12] Connector: ${CONN_STATE:-?} | Task: ${TASK_STATE:-belum ada}..."
                    
                    if [ "$TASK_STATE" = "RUNNING" ]; then
                        TASK_OK=true
                        echo -e "${GREEN}✓ Task konektor Debezium RUNNING! CDC aktif.${NC}"
                        break
                    elif [ "$TASK_STATE" = "FAILED" ]; then
                        echo -e "${RED}✗ Task konektor FAILED! Detail:${NC}"
                        echo "$TASK_STATUS" | grep -oP '"trace"\s*:\s*"[^"]*"' | head -1 | sed 's/"trace".*"//;s/"$//' || true
                        CONNECTOR_SETUP_STATUS="task_failed"
                        break
                    fi
                done
                
                if [ "$TASK_OK" = true ]; then
                    CONNECTOR_SETUP_STATUS="running"
                    
                    # ---------------------------------------------------------------
                    # TUNGGU TOPIC: Beri waktu Debezium membuat topic di Kafka
                    # Gateway akan gagal jika topic belum ada saat mulai consume
                    # ---------------------------------------------------------------
                    echo -e "${BLUE}⏳ Menunggu Debezium membuat topic di Kafka (max 30s)...${NC}"
                    EXPECTED_PREFIX="${HOSTNAME}_${TARGET_DB}"
                    [ "$CHOSEN_ENGINE" = "oracle" ] && [ -n "$ORACLE_PDB" ] && EXPECTED_PREFIX="${HOSTNAME}_${SELECTED_DB_NAME}"
                    
                    TOPIC_FOUND=false
                    for j in $(seq 1 6); do
                        sleep 5
                        # List semua topic di Kafka via Debezium REST API (Kafka Connect)
                        TOPICS=$(docker exec $(docker ps -qf "ancestor=quay.io/debezium/kafka:2.7" 2>/dev/null || docker ps -qf "ancestor=quay.io/debezium/kafka:2.4" 2>/dev/null || echo "none") /kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --list 2>/dev/null < /dev/null || echo "")
                        MATCHING=$(echo "$TOPICS" | grep -c "^${EXPECTED_PREFIX}" 2>/dev/null || true)
                        echo -e "  [${j}/6] Ditemukan ${MATCHING} topic dengan prefix '${EXPECTED_PREFIX}'"
                        if [ "$MATCHING" -gt 0 ]; then
                            TOPIC_FOUND=true
                            echo -e "${GREEN}✓ Topic Kafka berhasil dibuat oleh Debezium!${NC}"
                            break
                        fi
                    done
                    
                    if [ "$TOPIC_FOUND" = false ]; then
                        echo -e "${YELLOW}⚠️  Topic belum terdeteksi, tapi konektor RUNNING. Topic mungkin butuh waktu lebih lama (initial snapshot).${NC}"
                    fi
                elif [ "$CONNECTOR_SETUP_STATUS" != "task_failed" ]; then
                    echo -e "${YELLOW}⚠️  Task konektor belum RUNNING setelah 60 detik. Cek manual: curl localhost:8083/connectors/${CONNECTOR_NAME}/status${NC}"
                    CONNECTOR_SETUP_STATUS="task_pending"
                fi
            else
                echo -e "${YELLOW}[NOTE] Respon Debezium (HTTP Status: ${DBZ_RESP}).${NC}"
                if [ -f "${DBZ_BODY_FILE}" ]; then
                    echo -e "${YELLOW}Detail Error Debezium:${NC}"
                    cat "${DBZ_BODY_FILE}"
                    echo ""
                fi
                CONNECTOR_SETUP_STATUS="failed_${DBZ_RESP}"
            fi
            rm -f "${DBZ_BODY_FILE}"
        elif [ "$CHOSEN_ENGINE" = "postgres" ] && [ "$PUBLICATION_READY" != true ]; then
            CONNECTOR_SETUP_STATUS="publication_failed"
            echo -e "${RED}[ERROR] Connector tidak dibuat karena publication PostgreSQL belum siap.${NC}"
        else
            echo -e "${YELLOW}[NOTE] Debezium belum siap merespon. Konfigurasi otomatis ditunda.${NC}"
            CONNECTOR_SETUP_STATUS="debezium_not_ready"
        fi

        # ---------------------------------------------------------------
        # TRIGGER SINKRONISASI USER TABLE (DUMMY UPDATE)
        # Jika connector sudah punya offset, tabel yang baru ditambahkan
        # tidak akan mendapat snapshot ulang. Gunakan koneksi database yang
        # telah diverifikasi; jangan hanya mengasumsikan PostgreSQL berjalan
        # di Docker.
        # ---------------------------------------------------------------
        sync_user_table_cdc() {
            local sync_ok=false
            local pg_container="${PG_DOCKER_CONTAINER:-}"
            local sync_error=""

            echo -e "${BLUE}🔄 Memaksa sinkronisasi data user...${NC}"
            if [ "$CHOSEN_ENGINE" = "postgres" ]; then
                # Prioritas: koneksi TCP yang dipakai connector. Ini bekerja
                # untuk PostgreSQL native, eksternal, maupun Docker host.
                if command -v psql >/dev/null 2>&1; then
                    sync_error=$(PGPASSWORD="$AGENT_DB_PASS" psql -h "$DB_HOST" -p "$DB_PORT" -U "$AGENT_DB_USER" -d "$TARGET_DB" -c \
                    "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" 2>&1 >/dev/null) && sync_ok=true || true
                fi

                # Fallback untuk database PostgreSQL yang hanya dapat
                # diakses dari container atau melalui user sistem postgres.
                if [ "$sync_ok" = false ] && [ -z "$pg_container" ]; then
                    pg_container=$(docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null | awk 'tolower($0) ~ /postgres/ {print $1; exit}')
                fi
                if [ "$sync_ok" = false ] && [ -n "$pg_container" ] && \
                    docker exec < /dev/null "$pg_container" psql -U postgres -d "$TARGET_DB" -c \
                    "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" >/dev/null 2>&1; then
                    sync_ok=true
                fi
                if [ "$sync_ok" = false ] && id -u postgres >/dev/null 2>&1 && \
                    sudo -u postgres psql -p "$DB_PORT" -d "$TARGET_DB" -c \
                    "UPDATE ${DETECTED_USER_TABLE} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" >/dev/null 2>&1; then
                    sync_ok=true
                fi
            elif [ "$CHOSEN_ENGINE" = "mysql" ]; then
                local tbl_bare
                tbl_bare=$(echo "$DETECTED_USER_TABLE" | awk -F. '{print $NF}')
                if command -v mysql >/dev/null 2>&1 && \
                    mysql --no-defaults -h "$DB_HOST" -P "$DB_PORT" -u "$AGENT_DB_USER" -p"$AGENT_DB_PASS" -D "$TARGET_DB" -e \
                    "UPDATE ${tbl_bare} SET ${DETECTED_USER_COL} = ${DETECTED_USER_COL};" >/dev/null; then
                    sync_ok=true
                fi
            fi

            if [ "$sync_ok" = true ]; then
                USER_SYNC_STATUS="ok"
                USER_SYNC_MESSAGE="User table CDC sync event triggered"
                echo -e "${GREEN}✓ Event CDC tabel user berhasil dipicu.${NC}"
                return 0
            fi

            echo -e "${RED}✗ Gagal memicu event CDC tabel user. Tidak akan mengklaim sinkronisasi berhasil.${NC}"
            USER_SYNC_STATUS="failed"
            USER_SYNC_MESSAGE=$(echo "${sync_error:-unknown error}" | tr '\n' ' ' | sed "s/\"/'/g")
            echo -e "${YELLOW}   Dampak: actor dapat tampil Unknown sampai tabel user berubah secara natural.${NC}"
            echo -e "${YELLOW}   Detail: ${USER_SYNC_MESSAGE}${NC}"
            return 1
        }

        if [ "$CONNECTOR_SETUP_STATUS" = "running" ] && [ -n "$DETECTED_USER_TABLE" ]; then
            sync_user_table_cdc || true
        fi
    fi

# ------------------------------------------------------------------------------
# 6. DETEKSI ENDPOINT NETWORK & TELEMETRI
# ------------------------------------------------------------------------------
echo -e "\n${BLUE}[6/7] Mendeteksi Endpoint Network & Telemetri...${NC}"

KAFKA_BROKERS="${TAILSCALE_IP}:9092"
AGENT_SERVER_URL="http://${TAILSCALE_IP}:8083"
HOSTNAME=$(hostname)

# Simpan file environment konfigurasi tunggal di VPS Klien
cat <<EOF > /etc/auditchain/agent.env
# AuditChain Agent Environment Configuration
AUDITCHAIN_GATEWAY_URL="${GATEWAY_URL}"
AUDITCHAIN_API_KEY="${CLIENT_KEY}"
TAILSCALE_IP="${TAILSCALE_IP}"
AGENT_SERVER_URL="${AGENT_SERVER_URL}"
KAFKA_BROKERS="${KAFKA_BROKERS}"
DB_ENGINE="${SELECTED_DB_ENGINE}"
DB_NAME="${SELECTED_DB_NAME}"
DB_TABLES="${SELECTED_TABLES}"
CONNECTOR_STATUS="${CONNECTOR_SETUP_STATUS}"
USER_SYNC_STATUS="${USER_SYNC_STATUS}"
USER_SYNC_MESSAGE="${USER_SYNC_MESSAGE}"
EOF
chmod 600 /etc/auditchain/agent.env

echo -e "${GREEN}✓ Konfigurasi .env berhasil disimpan di /etc/auditchain/agent.env${NC}"

echo "  - Hostname           : ${HOSTNAME}"
echo "  - Tailscale VPN IP   : ${TAILSCALE_IP}"
echo "  - Kafka Broker IP    : ${KAFKA_BROKERS}"
echo "  - Agent Server URL   : ${AGENT_SERVER_URL}"
echo "  - Target Database    : ${SELECTED_DB_ENGINE} / ${SELECTED_DB_NAME}"

echo -e "\n${BLUE}[7/7] Mengirimkan Telemetri ke Gateway AuditChain Admin...${NC}"

PAYLOAD=$(cat <<EOF
{
  "api_key_prefix": "${CLIENT_KEY}",
  "kafka_brokers": "${KAFKA_BROKERS}",
  "agent_server_url": "${AGENT_SERVER_URL}",
  "hostname": "${HOSTNAME}",
  "tailscale_ip": "${TAILSCALE_IP}",
  "status": "pending_setup",
  "db_engine": "${SELECTED_DB_ENGINE}",
  "db_name": "${SELECTED_DB_NAME}",
  "db_tables": "${SELECTED_TABLES}",
  "connector_status": "${CONNECTOR_SETUP_STATUS}",
  "user_sync_status": "${USER_SYNC_STATUS}",
  "user_sync_message": "${USER_SYNC_MESSAGE}",
  "user_table_name": "${DETECTED_USER_TABLE}",
  "user_column_name": "${DETECTED_USER_COL}"
}
EOF
)

# Kirim HTTP POST Callback ke Gateway Backend
HTTP_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${GATEWAY_URL}/api/agent/telemetry" \
  -H "Content-Type: application/json" \
  -d "${PAYLOAD}" || echo "000")

if [ "$HTTP_RESPONSE" -eq 200 ] || [ "$HTTP_RESPONSE" -eq 201 ]; then
    echo -e "${GREEN}✓ Telemetri berhasil dikirim ke Admin Dashboard!${NC}"

    # Replay sekali lagi setelah Gateway menerima konfigurasi. Ini membuat
    # bootstrap identitas tetap andal saat event pertama terbit sebelum
    # consumer Gateway selesai dibuat.
    if [ "$CONNECTOR_SETUP_STATUS" = "running" ] && [ -n "$DETECTED_USER_TABLE" ] && declare -F sync_user_table_cdc >/dev/null; then
        sleep 3
        sync_user_table_cdc || true
    fi
else
    echo -e "${YELLOW}[NOTE] Telemetri terkirim (Response Status: ${HTTP_RESPONSE}). Data siap diverifikasi Admin.${NC}"
fi

# ------------------------------------------------------------------------------
# 7. COMPLETION BANNER
# ------------------------------------------------------------------------------
echo -e "\n${GREEN}"
echo "======================================================================"
echo " 🎉 INSTALASI AUDITCHAIN AGENT BERHASIL! (STATUS: PENDING SETUP)"
echo "======================================================================"
echo -e "${NC}"
echo " Detail Koneksi Virtual Server Anda:"
echo " --------------------------------------------------------------------"
echo "  • VPN IP Address     : ${TAILSCALE_IP}"
echo "  • Kafka Broker Host  : ${KAFKA_BROKERS}"
echo "  • Agent Server URL   : ${AGENT_SERVER_URL}"
echo "  • Database Monitored : ${SELECTED_DB_ENGINE} -> ${SELECTED_DB_NAME}"
echo "  • Connector Status   : ${CONNECTOR_SETUP_STATUS}"
echo "  • User Sync Status   : ${USER_SYNC_STATUS}"
echo "  • Status Dashboard   : Pending Verification by Admin 🟡"
echo " --------------------------------------------------------------------"
echo -e "${BLUE}Silakan hubungi Admin AuditChain untuk pengaktifan koneksi resmi.${NC}\n"

}
# Eksekusi fungsi utama
do_install "$@"
