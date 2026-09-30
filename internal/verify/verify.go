package verify

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

var (
	errVerifyDatabase  = errors.New("verify database unavailable")
	errVerifyForbidden = errors.New("verify table not allowed")
)

type ReadProjection struct {
	Schema       string
	Table        string
	PrimaryKey   string
	StateColumns []string
}

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
	db                   *sql.DB
	verifyToken          string
	port                 string
	recoveryHandler      http.Handler
	metricsHandler       http.Handler
	readProjection       func(string) (ReadProjection, bool)
	tableEndpointEnabled bool
	maxResponseBytes     int64
}

func NewServer(db *sql.DB, verifyToken, port string) *Server {
	return &Server{db: db, verifyToken: verifyToken, port: port, maxResponseBytes: 1024 * 1024}
}

func (s *Server) SetRecoveryHandler(handler http.Handler) {
	s.recoveryHandler = handler
}

func (s *Server) SetMetricsHandler(handler http.Handler) {
	s.metricsHandler = handler
}

func (s *Server) SetReadProjection(lookup func(string) (ReadProjection, bool)) {
	s.readProjection = lookup
}

func (s *Server) EnableTableEndpoint(enabled bool) {
	s.tableEndpointEnabled = enabled
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Endpoint lama — audit_trail (untuk SIMRS)
	mux.HandleFunc("/verify/", s.handleVerify)

	// Endpoint baru — resource geospasial (untuk Satu Peta)
	// Format: GET /verify-resource/<table>/<id>
	mux.HandleFunc("/verify-resource/", s.handleVerifyResource)

	if s.tableEndpointEnabled {
		mux.HandleFunc("/table/", s.handleTable)
	} else {
		mux.HandleFunc("/table/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "table endpoint disabled", http.StatusNotFound)
		})
	}
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	if s.recoveryHandler != nil {
		mux.Handle("/recover/", s.recoveryHandler)
	}
	if s.metricsHandler != nil {
		mux.Handle("/metrics", s.metricsHandler)
	}
	return mux
}

func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              ":" + s.port,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
}

func (s *Server) Run(ctx context.Context) error {
	server := s.HTTPServer()

	addr := ":" + s.port
	log.Printf("🔍 [VerifyServer] Mendengarkan di %s", addr)
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Start is retained for callers that used the original Verify-Only server.
// New code should use Run so shutdown can drain in-flight requests.
func (s *Server) Start() {
	if err := s.Run(context.Background()); err != nil {
		log.Printf("❌ [VerifyServer] Gagal start: %v", err)
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
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Ekstrak table dan id dari path: /verify-resource/<table>/<id>
	path := strings.TrimPrefix(r.URL.Path, "/verify-resource/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[1]) > 256 {
		http.Error(w, "format path harus /verify-resource/<table>/<id>", http.StatusBadRequest)
		return
	}

	tableName := parts[0]
	resourceID := parts[1]

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rec, err := s.queryResource(ctx, tableName, resourceID)
	if err != nil {
		s.writeQueryError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, rec)
}

// IsValidSQLIdentifier memvalidasi apakah string aman digunakan sebagai identifier SQL (seperti nama tabel/kolom)
func IsValidSQLIdentifier(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	return identifierPattern.MatchString(name)
}

// queryResource mengambil satu baris dari tabel berdasarkan primary key
func (s *Server) queryResource(ctx context.Context, tableName, resourceID string) (ResourceRecord, error) {
	// 1. Validasi regex dasar untuk nama tabel (SQL Injection Prevention)
	if !IsValidSQLIdentifier(tableName) {
		log.Printf("[VerifyServer] Nama tabel tidak valid (karakter ilegal): %q", tableName)
		return ResourceRecord{}, fmt.Errorf("%w: invalid table", errVerifyForbidden)
	}

	// Jika db tidak diinisialisasi (misal di test mock)
	if s.db == nil {
		log.Printf("[VerifyServer] Database tidak terhubung (nil)")
		return ResourceRecord{}, errVerifyDatabase
	}

	var owner, actualTableName, pkCol string
	var columns []string
	var err error
	if s.readProjection != nil {
		projection, allowed := s.readProjection(tableName)
		if !allowed {
			return ResourceRecord{}, errVerifyForbidden
		}
		owner, actualTableName, pkCol, columns = projection.Schema, projection.Table, projection.PrimaryKey, append([]string(nil), projection.StateColumns...)
	} else {
		// Legacy read mode resolves a table dynamically for compatibility. When a
		// recovery policy is configured, the exact projection above is used.
		err = s.db.QueryRowContext(ctx, `
			SELECT owner, table_name
			FROM (
				SELECT owner, table_name
				FROM all_tables
				WHERE UPPER(table_name) = UPPER(:1)
				ORDER BY CASE WHEN owner = USER THEN 0 ELSE 1 END, owner
			)
			WHERE rownum = 1
		`, tableName).Scan(&owner, &actualTableName)
		if errors.Is(err, sql.ErrNoRows) {
			return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}, nil
		}
		if err != nil {
			log.Printf("[VerifyServer] Gagal mencari tabel %q: %v", tableName, err)
			return ResourceRecord{}, errVerifyDatabase
		}
		pkCol, err = s.findOraclePrimaryKeyStrict(ctx, owner, actualTableName)
		if err != nil {
			return ResourceRecord{}, errVerifyDatabase
		}
		if pkCol == "" {
			pkCol, err = s.findPKColumnStrict(ctx, owner, actualTableName, []string{"ogc_fid", "id", "_id", "fid", "gid", "objectid"})
			if err != nil {
				return ResourceRecord{}, errVerifyDatabase
			}
		}
		if pkCol == "" {
			return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}, nil
		}
		if !IsValidSQLIdentifier(pkCol) {
			return ResourceRecord{}, errVerifyDatabase
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT column_name
			FROM all_tab_columns
			WHERE UPPER(table_name) = UPPER(:1)
			AND owner = :2
			AND data_type NOT IN ('SDO_GEOMETRY', 'BLOB', 'CLOB', 'RAW', 'LONG')
			ORDER BY column_id
		`, actualTableName, owner)
		if err != nil {
			return ResourceRecord{}, errVerifyDatabase
		}
		defer rows.Close()
		for rows.Next() {
			var col string
			if err := rows.Scan(&col); err != nil {
				return ResourceRecord{}, errVerifyDatabase
			}
			if IsValidSQLIdentifier(col) {
				columns = append(columns, col)
			}
		}
		if err := rows.Err(); err != nil {
			return ResourceRecord{}, errVerifyDatabase
		}
	}

	if len(columns) == 0 || !IsValidSQLIdentifier(pkCol) {
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}, nil
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
		log.Printf("[VerifyServer] Gagal query tabel %q id=%q: %v", tableName, resourceID, err)
		return ResourceRecord{}, errVerifyDatabase
	}
	defer dataRows.Close()

	if !dataRows.Next() {
		if err := dataRows.Err(); err != nil {
			return ResourceRecord{}, errVerifyDatabase
		}
		return ResourceRecord{Found: false, Table: tableName, ID: resourceID, CheckedAt: time.Now()}, nil
	}

	// Scan hasil ke map
	cols, err := dataRows.Columns()
	if err != nil {
		log.Printf("[VerifyServer] Gagal get columns: %v", err)
		return ResourceRecord{}, errVerifyDatabase
	}

	values := make([]interface{}, len(cols))
	valuePtrs := make([]interface{}, len(cols))
	for i := range values {
		valuePtrs[i] = &values[i]
	}

	if err := dataRows.Scan(valuePtrs...); err != nil {
		log.Printf("[VerifyServer] Gagal scan values: %v", err)
		return ResourceRecord{}, errVerifyDatabase
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
	}, nil
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

func (s *Server) findOraclePrimaryKeyStrict(ctx context.Context, owner, tableName string) (string, error) {
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
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return pkCol, nil
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

func (s *Server) findPKColumnStrict(ctx context.Context, owner, tableName string, candidates []string) (string, error) {
	query := `
		SELECT column_name
		FROM all_tab_columns
		WHERE UPPER(table_name) = UPPER(:1)
		AND owner = :2
		AND UPPER(column_name) = UPPER(:3)
	`
	var lastErr error
	for _, candidate := range candidates {
		var colName string
		err := s.db.QueryRowContext(ctx, query, tableName, owner, candidate).Scan(&colName)
		if err == nil {
			return colName, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			lastErr = err
			break
		}
	}
	return "", lastErr
}

// handleVerify melayani GET /verify/<table>/<id> untuk database non-spasial (seperti SIMRS)
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Autentikasi
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Ekstrak table dan id dari path: /verify/<table>/<id>
	path := strings.TrimPrefix(r.URL.Path, "/verify/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || len(parts[1]) > 256 {
		http.Error(w, "format path harus /verify/<table>/<id>", http.StatusBadRequest)
		return
	}

	tableName := parts[0]
	resourceID := parts[1]

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	rec, err := s.queryResource(ctx, tableName, resourceID)
	if err != nil {
		s.writeQueryError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, rec)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.db == nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "database_unreachable", "message": "client database is unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.PingContext(ctx); err != nil {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "database_unreachable", "message": "client database is unavailable"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.verifyToken == "" {
		return true
	}
	header := r.Header.Get("Authorization")
	prefix := "Bearer "
	if len(header) != len(prefix)+len(s.verifyToken) || !strings.HasPrefix(header, prefix) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.verifyToken)) == 1
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "response encoding failed", http.StatusInternalServerError)
		return
	}
	if s.maxResponseBytes > 0 && int64(len(payload)) > s.maxResponseBytes {
		http.Error(w, "response too large", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(payload, '\n'))
}

func (s *Server) writeQueryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errVerifyForbidden):
		http.Error(w, "table not allowed", http.StatusForbidden)
	case errors.Is(err, errVerifyDatabase):
		w.Header().Set("Content-Type", "application/json")
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"code":    "database_unreachable",
			"message": "client database is unavailable",
		})
	default:
		http.Error(w, "verification failed", http.StatusInternalServerError)
	}
}

// handleTable melayani GET /table/<table> untuk mengambil semua baris dari tabel
func (s *Server) handleTable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.readProjection != nil {
		http.Error(w, "table endpoint disabled when a recovery read policy is active", http.StatusForbidden)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/table/")
	if path == "" || strings.Contains(path, "/") {
		http.Error(w, "format path harus /table/<table>", http.StatusBadRequest)
		return
	}

	tableName := path
	limit := 100
	if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > 1000 {
			http.Error(w, "limit must be between 1 and 1000", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	records := s.queryTablePage(ctx, tableName, limit)
	if records == nil {
		records = []ResourceRecord{}
	}
	s.writeJSON(w, http.StatusOK, records)
}

// queryTable mengambil seluruh baris dari tabel
func (s *Server) queryTable(ctx context.Context, tableName string) []ResourceRecord {
	return s.queryTablePage(ctx, tableName, 100)
}

func (s *Server) queryTablePage(ctx context.Context, tableName string, limit int) []ResourceRecord {
	if !IsValidSQLIdentifier(tableName) {
		log.Printf("[VerifyServer] Nama tabel tidak valid: %q", tableName)
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
		log.Printf("[VerifyServer] Tabel tidak ditemukan: %q", tableName)
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

	if limit < 1 || limit > 1000 {
		limit = 100
	}
	dataQuery := `SELECT ` + colList + ` FROM "` + owner + `"."` + actualTableName + `" WHERE ROWNUM <= ` + strconv.Itoa(limit)
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
