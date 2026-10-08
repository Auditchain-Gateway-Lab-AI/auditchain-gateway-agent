package kafkaconsumer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"go-blockchain-api/internal/engine/hasher"
	"go-blockchain-api/internal/engine/stats"
	"go-blockchain-api/internal/models"
	"go-blockchain-api/internal/storage/snapshotstore"

	"github.com/segmentio/kafka-go"
	"gorm.io/gorm"
)

// DebeziumOracleMessage adalah struktur message dari Kafka
// setelah unwrap ExtractNewRecordState
type DebeziumOracleMessage map[string]interface{}

type clientMapping struct {
	ActorField         string
	FallbackActorField string
	ActionField        string
	ResourceField      string
	CreateActorField   string
	UpdateActorField   string
	DeleteActorField   string
}

type Engine struct {
	DB *gorm.DB
	// StartOffset is explicitly configured as "earliest" or "latest" before
	// consumers are allowed to start. It only applies when Kafka has no saved
	// offset for this consumer group/partition.
	StartOffset string

	// SnapshotBuilder is nil while the feature flag is disabled. Once enabled,
	// the builder creates an encrypted outbox payload before the database
	// transaction commits, so a new log cannot exist without its recovery job.
	SnapshotBuilder snapshotstore.OutboxBuilder

	sourceSystemCache sync.Map
	mappingCache      sync.Map
	actorCache        sync.Map // key: "clientID:actorID" → resolved name

	mu        sync.Mutex
	consumers map[string]*runningConsumer // key: ClientID
}

func (e *Engine) resolveClientMapping(clientID string) clientMapping {
	var m clientMapping
	if err := e.DB.Table("clients").
		Select("actor_field, fallback_actor_field, create_actor_field, update_actor_field, delete_actor_field, action_field, resource_field").
		Where("id = ?", clientID).
		Scan(&m).Error; err == nil {
		// Mapping dapat diubah dari Gateway Portal; baca konfigurasi terbaru
		// agar perubahan berlaku tanpa restart consumer.
		return m
	}
	return m
}

type runningConsumer struct {
	cancel      context.CancelFunc
	fingerprint string
}

func (e *Engine) resolveSourceSystem(cfg models.ClientKafkaConfig) string {
	if cached, ok := e.sourceSystemCache.Load(cfg.ClientID); ok {
		return cached.(string)
	}

	resolved := cfg.SourceSystem
	var companyName string
	if err := e.DB.Table("clients").
		Select("company_name").
		Where("id = ?", cfg.ClientID).
		Scan(&companyName).Error; err == nil && companyName != "" {
		resolved = companyName
	}

	e.sourceSystemCache.Store(cfg.ClientID, resolved)
	return resolved
}

func configFingerprint(cfg models.ClientKafkaConfig) string {
	return cfg.TopicPrefix + "|" + cfg.KafkaBrokers + "|" + cfg.ActorField + "|" + cfg.PKField
}

func parseKafkaStartOffset(value string) (int64, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "earliest":
		return kafka.FirstOffset, nil
	case "latest":
		return kafka.LastOffset, nil
	default:
		return 0, fmt.Errorf("KAFKA_CONSUMER_START_OFFSET harus eksplisit bernilai earliest atau latest")
	}
}

func kafkaMessageLogID(clientID string, msg kafka.Message) string {
	identity := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%x", clientID, msg.Topic, msg.Partition, msg.Offset, sha256.Sum256(msg.Value))
	sum := sha256.Sum256([]byte(identity))
	return "kafka-" + hex.EncodeToString(sum[:])
}

type kafkaMessageCommitter interface {
	CommitMessages(context.Context, ...kafka.Message) error
}

// processAndCommit provides at-least-once delivery: a message is committed
// only after its database transaction succeeds. If the commit result is
// uncertain, the stable Kafka-derived log ID makes replay safe.
func processAndCommit(ctx context.Context, committer kafkaMessageCommitter, msg kafka.Message, process func(kafka.Message) error) error {
	if err := process(msg); err != nil {
		return fmt.Errorf("process Kafka message %s[%d] offset %d: %w", msg.Topic, msg.Partition, msg.Offset, err)
	}
	if err := committer.CommitMessages(ctx, msg); err != nil {
		return fmt.Errorf("commit Kafka message %s[%d] offset %d: %w", msg.Topic, msg.Partition, msg.Offset, err)
	}
	return nil
}

// StartConsumers memulai consumer untuk semua klien yang punya ClientKafkaConfig aktif
func (e *Engine) Reconcile(ctx context.Context) error {
	var configs []models.ClientKafkaConfig
	if err := e.DB.Where("is_active = true").Find(&configs).Error; err != nil {
		return fmt.Errorf("gagal load kafka configs: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.consumers == nil {
		e.consumers = make(map[string]*runningConsumer)
	}

	seen := make(map[string]bool, len(configs))

	for _, cfg := range configs {
		seen[cfg.ClientID] = true
		fp := configFingerprint(cfg)

		if existing, running := e.consumers[cfg.ClientID]; running {
			// Hapus cache mapping & source system agar perubahan di admin panel segera terbaca
			e.mappingCache.Delete(cfg.ClientID)
			e.sourceSystemCache.Delete(cfg.ClientID)

			if existing.fingerprint == fp {
				continue // tidak ada perubahan, biarkan goroutine yang sudah jalan
			}
			log.Printf("🔄 [KafkaConsumer] Config klien=%s berubah, restart consumer...", cfg.ClientID)
			existing.cancel()
		}

		consumerCtx, cancel := context.WithCancel(ctx)
		e.consumers[cfg.ClientID] = &runningConsumer{cancel: cancel, fingerprint: fp}
		go e.startClientConsumer(consumerCtx, cfg)
	}

	// Stop consumer untuk klien yang config-nya sudah dihapus/dinonaktifkan
	// sejak siklus reconcile sebelumnya — inilah yang tadinya hilang.
	for clientID, rc := range e.consumers {
		if !seen[clientID] {
			log.Printf("🛑 [KafkaConsumer] Config klien=%s sudah tidak aktif, menghentikan consumer...", clientID)
			rc.cancel()
			delete(e.consumers, clientID)
		}
	}

	return nil
}

// startClientConsumer consume topic untuk satu klien
func (e *Engine) startClientConsumer(ctx context.Context, cfg models.ClientKafkaConfig) {
	log.Printf("🎧 [KafkaConsumer] Memulai consumer klien=%s prefix=%s", cfg.ClientID, cfg.TopicPrefix)

	for {
		select {
		case <-ctx.Done():
			log.Printf("🛑 [KafkaConsumer] Consumer klien=%s berhenti", cfg.ClientID)
			return
		default:
			if err := e.discoverAndConsume(ctx, cfg); err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("⚠️  [KafkaConsumer] Error klien=%s: %v, retry 5s...", cfg.ClientID, err)
				time.Sleep(5 * time.Second)
			}
		}
	}
}

// discoverAndConsume discover topic lalu consume
func (e *Engine) discoverAndConsume(ctx context.Context, cfg models.ClientKafkaConfig) error {
	startOffset, err := parseKafkaStartOffset(e.StartOffset)
	if err != nil {
		return err
	}
	dialer := getDialer(cfg)
	conn, err := dialer.DialContext(ctx, "tcp", cfg.KafkaBrokers)
	if err != nil {
		return fmt.Errorf("gagal konek Kafka: %w", err)
	}
	partitions, err := conn.ReadPartitions()
	conn.Close()
	if err != nil {
		return fmt.Errorf("gagal baca partisi: %w", err)
	}

	topicSet := make(map[string]struct{})
	for _, p := range partitions {
		if strings.HasPrefix(p.Topic, cfg.TopicPrefix) &&
			!strings.Contains(p.Topic, "schema_history") &&
			!strings.Contains(p.Topic, "heartbeat") {
			topicSet[p.Topic] = struct{}{}
		}
	}

	if len(topicSet) == 0 {
		// Debug: tampilkan semua topic yang tersedia di broker agar mudah diagnosa mismatch prefix
		allTopics := make([]string, 0)
		for _, p := range partitions {
			found := false
			for _, at := range allTopics {
				if at == p.Topic {
					found = true
					break
				}
			}
			if !found {
				allTopics = append(allTopics, p.Topic)
			}
		}
		log.Printf("⚠️  [KafkaConsumer] Belum ada topic prefix=%s (tersedia di Kafka: %v)", cfg.TopicPrefix, allTopics)
		time.Sleep(30 * time.Second)
		return nil
	}

	topics := make([]string, 0, len(topicSet))
	for t := range topicSet {
		topics = append(topics, t)
	}

	log.Printf("📋 [KafkaConsumer] klien=%s ditemukan %d topic: %v", cfg.ClientID, len(topics), topics)

	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:     []string{cfg.KafkaBrokers},
		GroupID:     fmt.Sprintf("auditchain-gateway-%s", cfg.ClientID),
		GroupTopics: topics,
		MinBytes:    1,
		MaxBytes:    10e6,
		// Zero means CommitMessages waits for the broker response instead of
		// queueing an asynchronous commit whose failure may be observed later.
		CommitInterval: 0,
		StartOffset:    startOffset,
		Dialer:         dialer,
	})
	defer reader.Close()

	log.Printf("✅ [KafkaConsumer] klien=%s siap consume %d topic", cfg.ClientID, len(topics))

	// Re-discovery setiap 60 detik untuk mendeteksi topic baru
	rediscoverTicker := time.NewTicker(60 * time.Second)
	defer rediscoverTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-rediscoverTicker.C:
			newTopics := e.discoverTopics(cfg)
			if len(newTopics) != len(topics) {
				log.Printf("🔄 [KafkaConsumer] klien=%s topic baru terdeteksi (%d→%d), restart consumer...",
					cfg.ClientID, len(topics), len(newTopics))
				return nil
			}

		default:
			fetchCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			msg, err := reader.FetchMessage(fetchCtx)
			cancel()

			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if err != context.DeadlineExceeded && err != context.Canceled {
					log.Printf("⚠️  [KafkaConsumer] Gagal fetch message klien=%s: %v", cfg.ClientID, err)
					time.Sleep(1 * time.Second)
				}
				continue
			}

			if err := processAndCommit(ctx, reader, msg, func(message kafka.Message) error {
				return e.processMessage(message, cfg)
			}); err != nil {
				// Stop this reader. Retrying a later offset in the same partition
				// could commit past a message that was not durably processed.
				return err
			}
		}
	}
}

// discoverTopics helper untuk cek daftar topic terkini
func (e *Engine) discoverTopics(cfg models.ClientKafkaConfig) []string {
	dialer := getDialer(cfg)
	conn, err := dialer.Dial("tcp", cfg.KafkaBrokers)
	if err != nil {
		return nil
	}
	partitions, err := conn.ReadPartitions()
	conn.Close()
	if err != nil {
		return nil
	}

	topicSet := make(map[string]struct{})
	for _, p := range partitions {
		if strings.HasPrefix(p.Topic, cfg.TopicPrefix) &&
			!strings.Contains(p.Topic, "schema_history") &&
			!strings.Contains(p.Topic, "heartbeat") {
			topicSet[p.Topic] = struct{}{}
		}
	}

	topics := make([]string, 0, len(topicSet))
	for t := range topicSet {
		topics = append(topics, t)
	}
	return topics
}

// processMessage memproses satu message Kafka menjadi AuditLog
func (e *Engine) processMessage(msg kafka.Message, cfg models.ClientKafkaConfig) error {
	if len(msg.Value) == 0 {
		return nil
	}

	logID := kafkaMessageLogID(cfg.ClientID, msg)
	var existing models.AuditLog
	lookupErr := e.DB.Select("log_id").Where("log_id = ?", logID).First(&existing).Error
	if lookupErr == nil {
		// The DB transaction committed but Kafka's offset commit may not have;
		// treating this replay as success lets the consumer safely commit it.
		return nil
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		return fmt.Errorf("gagal memeriksa replay Kafka: %w", lookupErr)
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(msg.Value, &rawMap); err != nil {
		return fmt.Errorf("payload CDC bukan JSON valid: %w", err)
	}
	if rawMap == nil {
		return fmt.Errorf("payload CDC harus berupa object JSON")
	}

	var payload DebeziumOracleMessage
	if innerPayload, exists := rawMap["payload"]; exists {
		if innerMap, ok := innerPayload.(map[string]interface{}); ok {
			payload = innerMap
		}
	}
	if payload == nil {
		payload = rawMap
	}

	op, _ := payload["__op"].(string)
	if op == "" {
		op, _ = payload["op"].(string)
	}

	tableName, _ := payload["__table"].(string)
	if tableName == "" {
		tableName, _ = payload["__collection"].(string)
	}
	if tableName == "" {
		tableName, _ = payload["table"].(string)
	}
	if tableName == "" {
		tableName, _ = payload["collection"].(string)
	}

	if tableName == "" {
		// Fallback parse dari topic name: "client_topic_prefix.schema.table"
		parts := strings.Split(msg.Topic, ".")
		if len(parts) > 0 {
			tableName = parts[len(parts)-1]
		}
	}
	if tableName == "" {
		return fmt.Errorf("nama tabel tidak ditemukan pada payload atau topic")
	}

	var agentCfg models.AgentConfig
	e.DB.Where("client_id = ?", cfg.ClientID).First(&agentCfg)

	// Proses CDC tabel user: jika admin sudah konfigurasi UserTableName, gunakan itu.
	// Jika belum, auto-detect berdasarkan nama tabel (user, account, member, dll)
	isUserTable := false
	if agentCfg.UserTableName != "" {
		// agentCfg.UserTableName bisa berupa "public.users" atau "dbo.users"
		// sedangkan tableName dari Debezium mungkin cuma "users".
		parts := strings.Split(agentCfg.UserTableName, ".")
		baseTarget := parts[len(parts)-1]

		isUserTable = strings.EqualFold(tableName, baseTarget) ||
			strings.Contains(agentCfg.UserTableName, tableName) ||
			strings.Contains(tableName, agentCfg.UserTableName)
	} else {
		lowerTable := strings.ToLower(tableName)
		for _, keyword := range []string{"user", "account", "akun", "pengguna", "member", "employee", "karyawan"} {
			if strings.Contains(lowerTable, keyword) {
				isUserTable = true
				break
			}
		}
	}

	// Untuk tabel user, kita WAJIB memproses operasi "r" (snapshot read)
	// agar memori identitas Gateway terisi dengan user yang sudah ada.
	if isUserTable {
		e.processClientUserCDC(payload, cfg, tableName, agentCfg)
	}

	// Untuk tabel non-user, skip operasi "r" (jangan catat snapshot awal sebagai audit log)
	if op == "r" {
		return nil
	}

	userName, _ := payload["__user_name"].(string)
	tsMs, _ := payload["__ts_ms"].(float64)

	mapping := e.resolveClientMapping(cfg.ClientID)

	action := opToAction(op)

	pkField := cfg.PKField
	if pkField == "" {
		pkField = "ID"
	}
	resourceID := findPrimaryKey(payload, pkField)
	resource := fmt.Sprintf("%s:%s", tableName, resourceID)

	actor := userName
	actorFound := false
	// Mapping khusus operasi memiliki prioritas di atas mapping umum.
	operationField := mapping.UpdateActorField
	if action == "INSERT" {
		operationField = mapping.CreateActorField
	}
	if action == "DELETE" {
		operationField = mapping.DeleteActorField
	}
	if operationField != "" {
		if value, ok := findFieldInsensitive(payload, operationField); ok && value != nil {
			if resolved := extractScalarValue(operationField, value); resolved != "" {
				actor, actorFound = resolved, true
			}
		}
	}

	if !actorFound && mapping.ActorField != "" {
		if customActor, ok := findFieldInsensitive(payload, mapping.ActorField); ok && customActor != nil {
			if resolved := extractScalarValue(mapping.ActorField, customActor); resolved != "" {
				actor, actorFound = resolved, true
			}
		}
	}

	if !actorFound && mapping.FallbackActorField != "" {
		if fallbackActor, ok := findFieldInsensitive(payload, mapping.FallbackActorField); ok && fallbackActor != nil {
			if resolved := extractScalarValue(mapping.FallbackActorField, fallbackActor); resolved != "" {
				actor, actorFound = resolved, true
			}
		}
	}

	// Auto-detect common actor columns jika admin tidak mensettingnya
	if !actorFound {
		// Prioritaskan deleted_by untuk operasi DELETE
		commonFields := []string{"updated_by", "deleted_by", "modified_by", "created_by", "actor", "user_id", "username", "author", "userid", "updatedby", "createdby", "deletedby"}
		if action == "DELETE" {
			commonFields = []string{"deleted_by", "deletedby", "updated_by", "modified_by", "actor", "user_id", "username", "author", "userid", "updatedby", "created_by", "createdby"}
		}
		for _, field := range commonFields {
			if autoActor, ok := findFieldInsensitive(payload, field); ok && autoActor != nil {
				actor = extractScalarValue(field, autoActor)
				actorFound = true
				break
			}
		}
	}

	// Fallback khusus DELETE: Debezium ExtractNewRecordState menghapus data row
	// pada event DELETE, sehingga kolom actor (updated_by, dll) tidak tersedia.
	// Strategi fallback:
	//   1. Cek "before" state (tersedia jika REPLICA IDENTITY FULL diaktifkan)
	//   2. Cari actor dari audit log terakhir untuk resource yang sama
	if !actorFound && (actor == "" || actor == "Unknown") && action == "DELETE" {
		// Strategi 1: Cari dari "before" payload (jika REPLICA IDENTITY FULL aktif)
		if before, ok := payload["before"].(map[string]interface{}); ok {
			deleteActorFields := []string{"deleted_by", "deletedby", "updated_by", "modified_by", "created_by", "actor", "user_id", "username", "author"}
			for _, field := range deleteActorFields {
				if val, exists := before[field]; exists && val != nil {
					resolved := extractScalarValue(field, val)
					if resolved != "" {
						actor = resolved
						actorFound = true
						break
					}
				}
			}
		}

		// Strategi 2: Fallback ke audit log terakhir untuk resource yang sama
		if !actorFound {
			var lastLog models.AuditLog
			if err := e.DB.Where(
				"client_id = ? AND resource = ? AND actor != 'Unknown' AND actor != ''",
				cfg.ClientID, resource,
			).Order("timestamp DESC").First(&lastLog).Error; err == nil && lastLog.Actor != "" {
				actor = lastLog.Actor
				actorFound = true
			}
		}
	}

	if !actorFound && actor == "" {
		actor = "Unknown"
	}

	// Resolusi CUID/UUID → Nama Manusia
	// Jika actor terlihat seperti ID acak (Prisma CUID, UUID, dll),
	// coba cari nama aslinya di tabel client_users
	if actor != "" && actor != "Unknown" && looksLikeGeneratedID(actor) {
		if resolved := e.resolveActorName(cfg.ClientID, actor); resolved != "" {
			actor = resolved
		}
	}

	var timestamp time.Time
	if tsMs > 0 {
		timestamp = time.UnixMilli(int64(tsMs))
	} else {
		timestamp = time.Now()
	}

	// Ekstrak dan canonicalize metadata — satu kali marshal, tidak double
	metadata := extractMetadata(payload)
	metaBytes, _ := json.Marshal(metadata)
	canonicalMeta := string(metaBytes) // ← tidak perlu unmarshal+marshal ulang

	// source_system = Client.CompanyName (diadopsi dari branch testing),
	// fallback ke cfg.SourceSystem jika company_name klien belum diisi.
	sourceSystem := e.resolveSourceSystem(cfg)

	auditLog := &models.AuditLog{
		LogID:        logID,
		ClientID:     cfg.ClientID,
		Actor:        actor,
		Action:       action,
		Resource:     resource,
		Timestamp:    timestamp,
		SourceSystem: sourceSystem,
		Metadata:     canonicalMeta,
		// AuthorizationContext sengaja dibiarkan "" untuk log dari Kafka
		// — konsisten dengan normalisasi di generateLogHash
		AuthorizationContext: "",
		Status:               "RECEIVED",
		IntegrityStatus:      models.IntegrityStatusNotChecked,
		IsLatest:             true,
	}

	// Hash menggunakan fungsi shared agar canonicalization konsisten
	auditLog.HashValue = hasher.GenerateLogHash(auditLog)
	auditLog.Status = "HASHED"
	auditLog.SnapshotStatus = models.SnapshotStatusPending

	var snapshotOutbox *models.SnapshotOutbox
	if e.SnapshotBuilder != nil {
		var err error
		snapshotOutbox, err = e.SnapshotBuilder.Build(*auditLog)
		if err != nil {
			return fmt.Errorf("gagal membentuk snapshot outbox untuk log %s: %w", auditLog.LogID, err)
		}
	}

	// Simpan audit log, pekerjaan snapshot, counter cache, dan Kafka offset
	// secara atomik. Tanpa transaksi, log dapat terlihat sudah tersimpan
	// walaupun snapshot recovery atau offset-nya gagal dibuat.
	//
	// SnapshotBuilder sengaja membuat payload terenkripsi sebelum transaksi,
	// tetapi object MinIO baru diunggah oleh worker setelah commit berhasil.
	err := e.DB.Transaction(func(tx *gorm.DB) error {
		// Reset is_latest for previous log
		if err := tx.Model(&models.AuditLog{}).
			Where("client_id = ? AND resource = ?", auditLog.ClientID, auditLog.Resource).
			Update("is_latest", false).Error; err != nil {
			return fmt.Errorf("gagal mereset is_latest: %w", err)
		}

		if err := tx.Create(auditLog).Error; err != nil {
			return fmt.Errorf("gagal simpan audit log: %w", err)
		}

		// Update dashboard stats
		if err := stats.RecordLogStats(tx, auditLog.ClientID, auditLog.Action); err != nil {
			log.Printf("?  [KafkaConsumer] Gagal update statistik dashboard: %v", err)
		}
		if snapshotOutbox != nil {
			if err := tx.Create(snapshotOutbox).Error; err != nil {
				return fmt.Errorf("gagal simpan snapshot outbox: %w", err)
			}
		}

		// Update counter cache di client_tables.
		tableNameOnly := tableName
		if tableNameOnly == "" {
			if strings.Contains(resource, ":") {
				tableNameOnly = strings.Split(resource, ":")[0]
			} else {
				tableNameOnly = resource
			}
		}
		upsertQuery := `
		INSERT INTO client_tables (client_id, table_name, row_count, last_action, last_actor, last_updated_at, created_at)
		VALUES (?, ?, CASE WHEN ? = 'INSERT' THEN 1 ELSE 0 END, ?, ?, ?, NOW())
		ON CONFLICT (client_id, table_name) DO UPDATE SET
			row_count = CASE 
				WHEN EXCLUDED.last_action = 'INSERT' THEN client_tables.row_count + 1
				WHEN EXCLUDED.last_action = 'DELETE' THEN GREATEST(client_tables.row_count - 1, 0)
				ELSE client_tables.row_count 
			END,
			last_action = EXCLUDED.last_action,
			last_actor = EXCLUDED.last_actor,
			last_updated_at = EXCLUDED.last_updated_at
		`
		if err := tx.Exec(upsertQuery, cfg.ClientID, tableNameOnly, action, action, actor, timestamp).Error; err != nil {
			return fmt.Errorf("gagal memperbarui counter table: %w", err)
		}

		// Simpan Kafka offset untuk verifikasi Lapis 3.
		kafkaOffset := &models.KafkaOffset{
			LogID:     auditLog.LogID,
			Topic:     msg.Topic,
			Partition: int32(msg.Partition),
			Offset:    msg.Offset,
		}
		if err := tx.Create(kafkaOffset).Error; err != nil {
			return fmt.Errorf("gagal simpan Kafka offset: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	log.Printf("✅ [KafkaConsumer] Tersimpan → engine=%-10s action=%-8s resource=%s client=%s source_system=%s",
		cfg.DBEngine, action, resource, cfg.ClientID, sourceSystem)

	return nil
}

func opToAction(op string) string {
	switch op {
	case "c":
		return "INSERT"
	case "u":
		return "UPDATE"
	case "d":
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

func findPrimaryKey(payload map[string]interface{}, pkField string) string {
	candidates := []string{pkField, "ID", "id", "ogc_fid", "_id", "fid"}

	for _, key := range candidates {
		val, ok := findFieldInsensitive(payload, key)
		if !ok || val == nil {
			continue
		}
		return extractScalarValue(key, val)
	}

	if rowID, ok := payload["__row_id"].(string); ok && rowID != "" {
		return rowID
	}
	return "unknown"
}

func findFieldInsensitive(payload map[string]interface{}, field string) (interface{}, bool) {
	if field == "" {
		return nil, false
	}

	if val, ok := payload[field]; ok {
		return val, true
	}

	lowerField := strings.ToLower(field)
	for k, v := range payload {
		if strings.ToLower(k) == lowerField {
			return v, true
		}
	}

	if after, ok := payload["after"].(map[string]interface{}); ok {
		if val, ok := after[field]; ok {
			return val, true
		}
		for k, v := range after {
			if strings.ToLower(k) == lowerField {
				return v, true
			}
		}
	}

	if before, ok := payload["before"].(map[string]interface{}); ok {
		if val, ok := before[field]; ok {
			return val, true
		}
		for k, v := range before {
			if strings.ToLower(k) == lowerField {
				return v, true
			}
		}
	}

	return nil, false
}

func isNumericColumn(key string) bool {
	k := strings.ToLower(key)
	if k == "user_input" || k == "user_updated" || k == "user_inserted" || k == "id_unit" {
		return true
	}
	if k == "id" || strings.HasPrefix(k, "id_") || strings.HasSuffix(k, "_id") || strings.HasSuffix(k, "id") {
		return true
	}
	return false
}

func decodeOracleNumberBase64(s string) (string, bool) {
	if len(s) == 0 || len(s) > 12 { // base64 pendek saja
		return "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(decoded) == 0 || len(decoded) > 8 {
		return "", false
	}
	var result int64
	for _, b := range decoded {
		result = result*256 + int64(b)
	}
	return fmt.Sprintf("%d", result), true
}

func extractScalarValue(key string, val interface{}) string {
	switch v := val.(type) {
	case string:
		if isNumericColumn(key) {
			if decoded, ok := decodeOracleNumberBase64(v); ok {
				return decoded
			}
		}
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%g", v)
	case map[string]interface{}:
		encoded, hasValue := v["value"].(string)
		if !hasValue || encoded == "" {
			return fmt.Sprintf("%v", v)
		}

		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return encoded
		}

		scale, hasScale := v["scale"].(float64)
		if hasScale && scale == 0 && len(decoded) <= 8 {
			var result int64
			for _, b := range decoded {
				result = result*256 + int64(b)
			}
			return fmt.Sprintf("%d", result)
		}

		s := strings.TrimRight(string(decoded), "\x00")
		if s != "" && isPrintable(s) {
			return s
		}
		return hex.EncodeToString(decoded)

	default:
		return fmt.Sprintf("%v", v)
	}
}

func isPrintable(s string) bool {
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return true
}

func cleanPayload(p DebeziumOracleMessage) DebeziumOracleMessage {
	res := make(DebeziumOracleMessage)
	for k, v := range p {
		if k == "__op" || k == "__table" || k == "__db" || k == "__schema" || k == "__ts_ms" || k == "__deleted" || k == "__user_name" || k == "__collection" {
			continue
		}
		if k == "op" || k == "table" || k == "db" || k == "schema" || k == "ts_ms" || k == "deleted" || k == "user_name" || k == "collection" {
			continue
		}

		// Redact sensitive fields
		lowerK := strings.ToLower(k)
		if strings.Contains(lowerK, "password") || strings.Contains(lowerK, "token") || strings.Contains(lowerK, "secret") || lowerK == "pin" || lowerK == "pass" {
			res[k] = "[REDACTED]"
			continue
		}

		res[k] = v
	}
	return res
}

func extractMetadata(payload map[string]interface{}) map[string]interface{} {
	skip := map[string]bool{
		"__op": true, "__table": true, "__db": true, "__schema": true,
		"__ts_ms": true, "__deleted": true, "__user_name": true,
		"__scn": true, "__tx_id": true, "__row_id": true,
		"op": true, "table": true, "db": true, "schema": true,
		"ts_ms": true, "deleted": true, "user_name": true,
		"__collection": true, "collection": true,
	}

	meta := make(map[string]interface{})
	for k, v := range payload {
		lowerK := strings.ToLower(k)
		if skip[lowerK] || skip[k] {
			continue
		}

		// Redact sensitive fields
		if strings.Contains(lowerK, "password") || strings.Contains(lowerK, "token") || strings.Contains(lowerK, "secret") || lowerK == "pin" || lowerK == "pass" {
			meta[lowerK] = "[REDACTED]"
			continue
		}

		meta[lowerK] = normalizeFieldValue(lowerK, v)
	}
	return meta
}

func normalizeFieldValue(key string, val interface{}) interface{} {
	switch v := val.(type) {
	case string:
		if isNumericColumn(key) {
			if decoded, ok := decodeOracleNumberBase64(v); ok {
				return decoded
			}
		}
		return v
	case map[string]interface{}:
		if _, hasValue := v["value"]; hasValue {
			return extractScalarValue(key, v)
		}
		result := make(map[string]interface{})
		for k, inner := range v {
			result[k] = normalizeFieldValue(k, inner)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = normalizeFieldValue(key, item)
		}
		return result
	default:
		return val
	}
}

// generateLogHash — format string HARUS identik dengan hasher.GenerateLogHash
// Normalisasi AuthorizationContext: "null"/"<nil>"/"" → selalu ""
type mapResolver struct {
	overrides map[string]string
}

func (r *mapResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if ip, ok := r.overrides[host]; ok {
		return []string{ip}, nil
	}
	return net.DefaultResolver.LookupHost(ctx, host)
}

func getDialer(cfg models.ClientKafkaConfig) *kafka.Dialer {
	overrides := make(map[string]string)
	brokers := strings.Split(cfg.KafkaBrokers, ",")
	for _, broker := range brokers {
		host, _, err := net.SplitHostPort(broker)
		if err == nil && host != "" {
			overrides["localhost"] = host
			overrides["127.0.0.1"] = host
		}
	}

	return &kafka.Dialer{
		Timeout:   10 * time.Second,
		DualStack: true,
		Resolver: &mapResolver{
			overrides: overrides,
		},
	}
}

func (e *Engine) processClientUserCDC(payload DebeziumOracleMessage, cfg models.ClientKafkaConfig, tableName string, agentCfg models.AgentConfig) {
	// Tentukan kolom identifier user — dari config admin, atau auto-detect
	userCol := agentCfg.UserColumnName
	if userCol == "" {
		// Auto-detect: cari kolom yang kemungkinan besar berisi identifier user
		candidates := []string{"username", "email", "login", "name", "nama", "user_name"}
		for _, c := range candidates {
			if raw, ok := findFieldInsensitive(payload, c); ok && raw != nil {
				userCol = c
				break
			}
		}
	}

	// Ambil user ID (primary key) — ini yang akan dipakai untuk mencocokkan actor
	userID := ""
	idCandidates := []string{"id", "user_id", "userid", "uid"}
	for _, idc := range idCandidates {
		if idRaw, ok := findFieldInsensitive(payload, idc); ok && idRaw != nil {
			userID = extractScalarValue(idc, idRaw)
			if userID != "" {
				break
			}
		}
	}

	// Ambil username/identifier dari kolom yang terdeteksi
	username := ""
	if userCol != "" {
		if usernameRaw, ok := findFieldInsensitive(payload, userCol); ok && usernameRaw != nil {
			username = extractScalarValue(userCol, usernameRaw)
		}
	}

	// Jika tidak ada ID maupun username, skip
	if userID == "" && username == "" {
		return
	}

	// Gunakan userID sebagai key utama (karena ini yang muncul di kolom actor tabel lain)
	// Fallback ke username jika tidak ada ID
	lookupKey := userID
	if lookupKey == "" {
		lookupKey = username
	}

	// Coba cari email dan fullname
	email := ""
	if emailRaw, ok := findFieldInsensitive(payload, "email"); ok {
		email = extractScalarValue("email", emailRaw)
	}

	fullName := ""
	for _, nameField := range []string{"name", "nama", "full_name", "fullname", "display_name"} {
		if nameRaw, ok := findFieldInsensitive(payload, nameField); ok {
			fullName = extractScalarValue(nameField, nameRaw)
			if fullName != "" {
				break
			}
		}
	}

	// Gabungkan first_name + last_name jika kolom 'name' tunggal tidak ada
	if fullName == "" {
		firstName := ""
		lastName := ""
		if fnRaw, ok := findFieldInsensitive(payload, "first_name"); ok && fnRaw != nil {
			firstName = extractScalarValue("first_name", fnRaw)
		}
		if lnRaw, ok := findFieldInsensitive(payload, "last_name"); ok && lnRaw != nil {
			lastName = extractScalarValue("last_name", lnRaw)
		}
		combined := strings.TrimSpace(firstName + " " + lastName)
		if combined != "" {
			fullName = combined
		}
	}

	// Jika fullName kosong tapi username ada dan bukan UUID/CUID, pakai username sebagai nama
	if fullName == "" && username != "" && !looksLikeGeneratedID(username) {
		fullName = username
	}

	rawJSON, _ := json.Marshal(payload)

	var clientUser models.ClientUser
	err := e.DB.Where("client_id = ? AND user_id = ?", cfg.ClientID, lookupKey).First(&clientUser).Error
	if err != nil {
		// Insert
		newUser := models.ClientUser{
			ClientID:    cfg.ClientID,
			UserID:      lookupKey,
			Username:    username,
			Email:       email,
			FullName:    fullName,
			SourceTable: tableName,
			RawData:     string(rawJSON),
			LastSeenAt:  time.Now(),
		}
		e.DB.Create(&newUser)
	} else {
		// Update
		updates := map[string]interface{}{
			"last_seen_at": time.Now(),
			"raw_data":     string(rawJSON),
		}
		if email != "" {
			updates["email"] = email
		}
		if fullName != "" {
			updates["full_name"] = fullName
		}
		e.DB.Model(&clientUser).Updates(updates)
	}

	// Invalidate actor cache agar resolusi langsung pakai data terbaru
	e.actorCache.Delete(cfg.ClientID + ":" + lookupKey)

	// AuditLog bersifat immutable setelah ditulis dan hash-nya dibuat. Jangan
	// melakukan backfill actor dengan mengubah baris audit_logs di sini:
	// actor adalah bagian dari formula hash, sehingga perubahan belakangan
	// akan membuat log sah terlihat tampered dan tidak lagi cocok dengan
	// anchor Fabric. Resolusi nama hanya berlaku saat event baru diproses;
	// histori lama tetap menyimpan actor asli yang ikut di-hash.
}

// looksLikeGeneratedID mendeteksi apakah string terlihat seperti CUID, UUID, atau ID acak lainnya
// sehingga perlu di-resolve ke nama manusia.
var (
	cuidPattern  = regexp.MustCompile(`^c[a-z0-9]{20,30}$`)                                             // Prisma CUID: cmsrsfohw000a6jjxh4jgty6d
	uuidPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`) // UUID v4
	hexIDPattern = regexp.MustCompile(`^[0-9a-f]{24,}$`)                                                // MongoDB ObjectId dll
)

func looksLikeGeneratedID(val string) bool {
	// Mengizinkan Numeric ID (Auto Increment) seperti "118" untuk diteruskan ke proses resolusi (query ke client_users)
	isNumeric := true
	for _, c := range val {
		if c < '0' || c > '9' {
			isNumeric = false
			break
		}
	}
	if isNumeric && len(val) > 0 {
		return true
	}

	if len(val) < 16 {
		return false // Terlalu pendek untuk string acak, kemungkinan bukan CUID/UUID
	}
	lower := strings.ToLower(val)
	return cuidPattern.MatchString(lower) || uuidPattern.MatchString(lower) || hexIDPattern.MatchString(lower)
}

// resolveActorName menerjemahkan ID acak (CUID/UUID) menjadi nama manusia
// dengan mencari di tabel client_users. Hasilnya di-cache agar tidak query DB berulang.
func (e *Engine) resolveActorName(clientID, actorID string) string {
	cacheKey := clientID + ":" + actorID

	// Cek cache dulu
	if cached, ok := e.actorCache.Load(cacheKey); ok {
		return cached.(string)
	}

	// Cari di client_users: cocokkan actorID dengan user_id
	var user models.ClientUser
	result := e.DB.Where("client_id = ? AND user_id = ?", clientID, actorID).Limit(1).Find(&user)
	if result.Error != nil {
		log.Printf("⚠️  [KafkaConsumer] Gagal mencari actor di client_users: %v", result.Error)
		return ""
	}
	if result.RowsAffected == 0 {
		// Fallback: coba cari di raw_data (mungkin ID disimpan dengan key berbeda)
		fallbackResult := e.DB.Where("client_id = ? AND CAST(raw_data AS TEXT) LIKE ?", clientID, "%"+actorID+"%").Limit(1).Find(&user)
		if fallbackResult.Error != nil {
			log.Printf("⚠️  [KafkaConsumer] Gagal fallback pencarian actor di raw_data: %v", fallbackResult.Error)
			return ""
		}
		if fallbackResult.RowsAffected == 0 {
			return ""
		}
	}

	// Prioritas: Email > FullName > Username
	resolved := ""
	if user.Email != "" {
		resolved = user.Email
	} else if user.FullName != "" {
		resolved = user.FullName
	}

	e.actorCache.Store(cacheKey, resolved)
	return resolved
}
