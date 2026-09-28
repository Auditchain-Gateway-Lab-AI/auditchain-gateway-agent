package verify

import (
	"context"
	"fmt"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ResourceRecord adalah data yang dikembalikan ke Gateway saat verifikasi.
// Berisi semua kolom non-geometry dari baris yang diminta.
type ResourceRecord struct {
	Found     bool                   `json:"found"`
	Table     string                 `json:"table"`
	ID        string                 `json:"id"`
	Data      map[string]interface{} `json:"data"`
	CheckedAt time.Time              `json:"checked_at"`
}

type Server struct {
	db          *sql.DB
	verifyToken string
	port        string
}

func NewServer(db *sql.DB, verifyToken, port string) *Server {
	return &Server{db: db, verifyToken: verifyToken, port: port}
}

func (s *Server) Start() {
	mux := http.NewServeMux()

	// Endpoint lama — audit_trail (untuk SIMRS)
	mux.HandleFunc("/verify/", s.handleVerify)

	// Endpoint baru — resource geospasial (untuk Satu Peta)
	// Format: GET /verify-resource/<table>/<id>
	mux.HandleFunc("/verify-resource/", s.handleVerifyResource)

	mux.HandleFunc("/table/", s.handleTable)
	mux.HandleFunc("/health", s.handleHealth)

	addr := ":" + s.port
	log.Printf("🔍 [VerifyServer] Mendengarkan di %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("❌ [VerifyServer] Gagal start: %v", err)
	}
}

// handleVerifyResource melayani GET /verify-resource/<table>/<id>
// Gateway memanggil ini dengan nama tabel dan id dari kolom resource (format: tabel:id)
func (s *Server) handleVerifyResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Autentikasi
	if s.verifyToken != "" {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+s.verifyToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Ekstrak table dan id dari path: /verify-resource/<table>/<id>
	path := strings.TrimPrefix(r.URL.Path, "/verify-resource/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "format path harus /verify-resource/<table>/<id>", http.StatusBadRequest)
		return
	}

	tableName := parts[0]
	resourceID := parts[1]

	rec := s.queryResource(r.Context(), tableName, resourceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

// IsValidSQLIdentifier memvalidasi apakah string aman digunakan sebagai identifier SQL (seperti nama tabel/kolom)
func IsValidSQLIdentifier(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	return regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`).MatchString(name)
}

// queryResource mengambil satu baris dari tabel berdasarkan primary key
func (s *Server) queryResource(ctx context.Context, tableName, resourceID string) ResourceRecord {
	// 1. Validasi regex dasar untuk nama tabel (SQL Injection Prevention)
	if !IsValidSQLIdentifier(tableName) {
		log.Printf("[VerifyServer] Nama tabel tidak valid (karakter ilegal): %s", tableName)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Jika db tidak diinisialisasi (misal di test mock)
	if s.db == nil {
		log.Printf("[VerifyServer] Database tidak terhubung (nil)")
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// 2. Validasi apakah tabel tersebut benar-benar ada di schema user saat ini atau schema lain (untuk Oracle)
	var owner, actualTableName string
	err := s.db.QueryRowContext(ctx, `
		SELECT owner, table_name 
		FROM (
			SELECT owner, table_name 
			FROM all_tables 
			WHERE UPPER(table_name) = UPPER(:1)
			ORDER BY CASE WHEN owner = USER THEN 0 ELSE 1 END, owner
		)
		WHERE rownum = 1
	`, tableName).Scan(&owner, &actualTableName)
	if err != nil {
		log.Printf("[VerifyServer] Tabel tidak ditemukan di database atau error: %s (err: %v)", tableName, err)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Cari kolom PK yang ada di tabel ini menggunakan constraint Oracle
	pkCol := s.findOraclePrimaryKey(ctx, owner, actualTableName)
	if pkCol == "" {
		// Fallback ke pkCandidates jika constraint PK tidak didefinisikan
		pkCandidates := []string{"ogc_fid", "id", "_id", "fid", "gid", "objectid"}
		pkCol = s.findPKColumn(ctx, owner, actualTableName, pkCandidates)
	}

	if pkCol == "" {
		log.Printf("[VerifyServer] Tidak ditemukan kolom PK di tabel %s.%s", owner, actualTableName)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Validasi regex kolom PK (tambahan proteksi)
	if !IsValidSQLIdentifier(pkCol) {
		log.Printf("[VerifyServer] Nama kolom PK tidak valid (karakter ilegal): %s", pkCol)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Query baris — exclude kolom geometry/LOB otomatis
	query := `
		SELECT column_name 
		FROM all_tab_columns 
		WHERE UPPER(table_name) = UPPER(:1) 
		AND owner = :2
		AND data_type NOT IN ('SDO_GEOMETRY', 'BLOB', 'CLOB', 'RAW', 'LONG')
		ORDER BY column_id
	`

	rows, err := s.db.QueryContext(ctx, query, actualTableName, owner)
	if err != nil {
		log.Printf("[VerifyServer] Gagal ambil kolom tabel %s: %v", tableName, err)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err == nil {
			// Hanya tambahkan jika nama kolom aman
			if IsValidSQLIdentifier(col) {
				columns = append(columns, col)
			}
		}
	}

	if len(columns) == 0 {
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Build SELECT query dengan kolom yang aman (selalu dibungkus quotes)
	colList := ""
	for i, col := range columns {
		if i > 0 {
			colList += ", "
		}
		colList += `"` + col + `"`
	}

	// Gunakan ROWNUM <= 1 untuk Oracle
	dataQuery := `SELECT ` + colList + ` FROM "` + owner + `"."` + actualTableName + `" WHERE "` + pkCol + `" = :1 AND ROWNUM <= 1`

	dataRows, err := s.db.QueryContext(ctx, dataQuery, resourceID)
	if err != nil {
		log.Printf("[VerifyServer] Gagal query tabel %s id=%s: %v", tableName, resourceID, err)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}
	defer dataRows.Close()

	if !dataRows.Next() {
		// Baris tidak ditemukan
		log.Printf("[VerifyServer] Baris tidak ditemukan: tabel=%s id=%s", tableName, resourceID)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	// Scan hasil ke map
	cols, err := dataRows.Columns()
	if err != nil {
		log.Printf("[VerifyServer] Gagal get columns: %v", err)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	values := make([]interface{}, len(cols))
	valuePtrs := make([]interface{}, len(cols))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	if err := dataRows.Scan(valuePtrs...); err != nil {
		log.Printf("[VerifyServer] Gagal scan values: %v", err)
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}
	}

	data := make(map[string]interface{})
	for i, colName := range cols {
		val := values[i]
		if b, ok := val.([]byte); ok {
			data[colName] = string(b)
		} else {
			data[colName] = val
		}
	}

	return ResourceRecord{
		Found:     true,
		Table:     tableName,
		ID:        resourceID,
		Data:      data,
		CheckedAt: time.Now(),
	}
}

// findOraclePrimaryKey mencari kolom primary key menggunakan Oracle constraint catalogs
func (s *Server) findOraclePrimaryKey(ctx context.Context, owner, tableName string) string {
	query := `
		SELECT cols.column_name
		FROM all_constraints cons
		JOIN all_cons_columns cols ON cons.constraint_name = cols.constraint_name AND cons.owner = cols.owner
		WHERE cons.constraint_type = 'P'
		  AND cons.owner = :1
		  AND cons.table_name = :2
		  AND rownum = 1
	`
	var pkCol string
	err := s.db.QueryRowContext(ctx, query, owner, tableName).Scan(&pkCol)
	if err == nil && pkCol != "" {
		return pkCol
	}
	return ""
}

// findPKColumn mencari kolom primary key yang ada di tabel menggunakan kandidat
func (s *Server) findPKColumn(ctx context.Context, owner, tableName string, candidates []string) string {
	query := `
		SELECT column_name 
		FROM all_tab_columns 
		WHERE UPPER(table_name) = UPPER(:1) 
		AND owner = :2 
		AND UPPER(column_name) = UPPER(:3)
	`

	var colName string
	for _, candidate := range candidates {
		err := s.db.QueryRowContext(ctx, query, tableName, owner, candidate).Scan(&colName)
		if err == nil {
			return colName
		}
	}
	return ""
}

// handleVerify melayani GET /verify/<table>/<id> untuk database non-spasial (seperti SIMRS)
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Autentikasi
	if s.verifyToken != "" {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+s.verifyToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	// Ekstrak table dan id dari path: /verify/<table>/<id>
	path := strings.TrimPrefix(r.URL.Path, "/verify/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "format path harus /verify/<table>/<id>", http.StatusBadRequest)
		return
	}

	tableName := parts[0]
	resourceID := parts[1]

	rec := s.queryResource(r.Context(), tableName, resourceID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}


// handleTable melayani GET /table/<table> untuk mengambil semua baris dari tabel
func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.verifyToken != "" {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+s.verifyToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	path := strings.TrimPrefix(r.URL.Path, "/table/")
	if path == "" || strings.Contains(path, "/") {
		http.Error(w, "format path harus /table/<table>", http.StatusBadRequest)
		return
	}

	tableName := path
	records := s.queryTable(r.Context(), tableName)
    if records == nil {
        records = []ResourceRecord{}
    }
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

// queryTable mengambil seluruh baris dari tabel
func (s *Server) queryTable(ctx context.Context, tableName string) []ResourceRecord {
	if !IsValidSQLIdentifier(tableName) {
		log.Printf("[VerifyServer] Nama tabel tidak valid: %s", tableName)
		return nil
	}

	if s.db == nil {
		return nil
	}

	var owner, actualTableName string
	err := s.db.QueryRowContext(ctx, `
		SELECT owner, table_name 
		FROM (
			SELECT owner, table_name 
			FROM all_tables 
			WHERE UPPER(table_name) = UPPER(:1)
			ORDER BY CASE WHEN owner = USER THEN 0 ELSE 1 END, owner
		)
		WHERE rownum = 1
	`, tableName).Scan(&owner, &actualTableName)
	
	if err != nil {
		log.Printf("[VerifyServer] Tabel tidak ditemukan: %s", tableName)
		return nil
	}

	pkCol := s.findOraclePrimaryKey(ctx, owner, actualTableName)
	if pkCol == "" {
		pkCandidates := []string{"ogc_fid", "id", "_id", "fid", "gid", "objectid"}
		pkCol = s.findPKColumn(ctx, owner, actualTableName, pkCandidates)
	}

	if pkCol == "" {
		return nil
	}

	query := `
		SELECT column_name 
		FROM all_tab_columns 
		WHERE UPPER(table_name) = UPPER(:1) 
		AND owner = :2
		AND data_type NOT IN ('SDO_GEOMETRY', 'BLOB', 'CLOB', 'RAW', 'LONG')
		ORDER BY column_id
	`
	rows, err := s.db.QueryContext(ctx, query, actualTableName, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var columns []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err == nil && IsValidSQLIdentifier(col) {
			columns = append(columns, col)
		}
	}

	if len(columns) == 0 {
		return nil
	}

	colList := ""
	pkIndex := -1
	for i, col := range columns {
		if i > 0 {
			colList += ", "
		}
		colList += `"` + col + `"`
		if strings.EqualFold(col, pkCol) {
			pkIndex = i
		}
	}
    if pkIndex == -1 {
        colList = `"` + pkCol + `", ` + colList
    }

	dataQuery := `SELECT ` + colList + ` FROM "` + owner + `"."` + actualTableName + `"`
	dataRows, err := s.db.QueryContext(ctx, dataQuery)
	if err != nil {
		return nil
	}
	defer dataRows.Close()

	cols, err := dataRows.Columns()
	if err != nil {
		return nil
	}

	var records []ResourceRecord

	for dataRows.Next() {
		values := make([]interface{}, len(cols))
		valuePtrs := make([]interface{}, len(cols))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := dataRows.Scan(valuePtrs...); err != nil {
			continue
		}

		data := make(map[string]interface{})
		var id string
		for i, colName := range cols {
			val := values[i]
			if b, ok := val.([]byte); ok {
				data[colName] = string(b)
			} else {
				data[colName] = val
			}

			if strings.EqualFold(colName, pkCol) {
				if b, ok := val.([]byte); ok {
					id = string(b)
				} else {
					id = fmt.Sprintf("%v", val)
				}
			}
		}

		records = append(records, ResourceRecord{
			Found:     true,
			Table:     tableName,
			ID:        id,
			Data:      data,
			CheckedAt: time.Now(),
		})
	}

	return records
}
