package normalizer

import (
	"encoding/json"
	"errors"
	"fmt"
	"go-blockchain-api/internal/models"
	"time"

	"github.com/google/uuid"
)

// ClientFieldMapping adalah kamus pemetaan field khusus untuk klien tertentu
// yang diambil dari database berdasarkan ClientID yang sedang login.
//
// Contoh untuk klien SIMRS yang menggunakan auditchain-agent:
//
//	ActorField    = "app_user"   (field nama pengguna aplikasi; fallback ke db_user jika kosong)
//	ActionField   = "operasi"    (INSERT / UPDATE / DELETE dari trigger)
//	ResourceField = "tabel"      (nama tabel yang berubah)
type ClientFieldMapping struct {
	ActorField    string `json:"actor_field"`
	ActionField   string `json:"action_field"`
	ResourceField string `json:"resource_field"`
	// FallbackActorField digunakan jika nilai ActorField kosong/null pada payload.
	// Contoh: actor_field = "app_user", fallback_actor_field = "db_user"
	FallbackActorField string `json:"fallback_actor_field"`
}

// RawLogInput adalah representasi data mentah yang dikirim oleh sistem klien
type RawLogInput struct {
	ClientID             string                 `json:"-"`
	LogID                string                 `json:"log_id"`
	Actor                string                 `json:"actor"`
	Action               string                 `json:"action"`
	Resource             string                 `json:"resource"`
	Timestamp            string                 `json:"timestamp"`
	SourceSystem         string                 `json:"source_system"`
	AuthorizationContext map[string]interface{} `json:"authorization_context"`
	Metadata             map[string]interface{} `json:"metadata"`
}

// MapDynamicPayload menerjemahkan JSON dinamis dari klien menjadi RawLogInput yang baku.
// Mendukung dua mode:
//  1. Payload sudah standar (actor/action/resource) — mapping = nil atau semua field kosong
//  2. Payload raw dengan nama field kustom — mapping berisi konfigurasi per klien
func MapDynamicPayload(dynamicPayload map[string]interface{}, mapping *ClientFieldMapping) (RawLogInput, error) {
	var input RawLogInput

	getString := func(key string) string {
		if val, ok := dynamicPayload[key]; ok {
			if val == nil {
				return ""
			}
			return fmt.Sprintf("%v", val)
		}
		return ""
	}

	// 1. Tentukan kunci berdasarkan mapping klien
	keyActor := "actor"
	keyFallbackActor := ""

	if mapping != nil {
		if mapping.ActorField != "" {
			keyActor = mapping.ActorField
		}
		keyFallbackActor = mapping.FallbackActorField
	}

	// 2. Ekstraksi nilai utama
	input.Actor = getString(keyActor)

	// Fallback actor: jika nilai field utama kosong, coba field fallback
	// Contoh: app_user kosong → gunakan db_user
	if input.Actor == "" && keyFallbackActor != "" {
		input.Actor = getString(keyFallbackActor)
	}

	input.Action = getString("action")
	input.Resource = getString("resource")
	input.SourceSystem = getString("source_system")
	input.LogID = getString("log_id")
	input.Timestamp = getString("timestamp")

	// 3. Ekstraksi Metadata
	if metaVal, exists := dynamicPayload["metadata"]; exists {
		if metaMap, ok := metaVal.(map[string]interface{}); ok {
			input.Metadata = metaMap
		} else {
			var tempMap map[string]interface{}
			if err := json.Unmarshal([]byte(fmt.Sprintf("%v", metaVal)), &tempMap); err == nil {
				input.Metadata = tempMap
			}
		}
	}

	return input, nil
}

// Normalize mengubah RawLogInput menjadi models.AuditLog yang standar
func Normalize(input RawLogInput) (*models.AuditLog, error) {
	if input.Actor == "" || input.Action == "" || input.Resource == "" || input.SourceSystem == "" {
		errMsg := fmt.Sprintf("field wajib kosong! Isi terbaca -> Actor: '%s', Action: '%s', Resource: '%s', SourceSystem: '%s'",
			input.Actor, input.Action, input.Resource, input.SourceSystem)
		return nil, errors.New(errMsg)
	}

	logID := input.LogID
	if logID == "" {
		logID = uuid.New().String()
	}

	logTime := time.Now()
	if input.Timestamp != "" {
		parsedTime, err := time.Parse(time.RFC3339, input.Timestamp)
		if err == nil {
			logTime = parsedTime
		}
	}

	authCtxBytes, _ := json.Marshal(input.AuthorizationContext)
	metaBytes, _ := json.Marshal(input.Metadata)

	standardLog := &models.AuditLog{
		LogID:                logID,
		Actor:                input.Actor,
		Action:               input.Action,
		Resource:             input.Resource,
		Timestamp:            logTime,
		SourceSystem:         input.SourceSystem,
		AuthorizationContext: string(authCtxBytes),
		Metadata:             string(metaBytes),
		Status:               "RECEIVED",
		IntegrityStatus:      models.IntegrityStatusNotChecked,
	}

	return standardLog, nil
}
