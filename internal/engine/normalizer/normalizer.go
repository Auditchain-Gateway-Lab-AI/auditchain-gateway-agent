package normalizer

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"auditchain-agent/internal/models"
)

// ClientFieldMapping maps client-specific actor, action, and resource names
// to the canonical audit-event fields.
type ClientFieldMapping struct {
	ActorField         string `json:"actor_field"`
	FallbackActorField string `json:"fallback_actor_field"`
	ActionField        string `json:"action_field"`
	ResourceField      string `json:"resource_field"`
}

// RawLogInput is the decoded event before it is normalized.
type RawLogInput struct {
	LogID                string
	ClientID             string
	Actor                string
	Action               string
	Resource             string
	Timestamp            string
	SourceSystem         string
	AuthorizationContext map[string]interface{}
	Metadata             map[string]interface{}
}

// MapDynamicPayload translates a client-specific payload into canonical
// fields. Empty mapping fields use the standard actor/action/resource names.
func MapDynamicPayload(payload map[string]interface{}, mapping *ClientFieldMapping) RawLogInput {
	getString := func(key string) string {
		value, ok := payload[key]
		if !ok || value == nil {
			return ""
		}
		return fmt.Sprintf("%v", value)
	}

	actorField, actionField, resourceField := "actor", "action", "resource"
	fallbackActorField := ""
	if mapping != nil {
		if mapping.ActorField != "" {
			actorField = mapping.ActorField
		}
		if mapping.ActionField != "" {
			actionField = mapping.ActionField
		}
		if mapping.ResourceField != "" {
			resourceField = mapping.ResourceField
		}
		fallbackActorField = mapping.FallbackActorField
	}

	input := RawLogInput{
		LogID:        getString("log_id"),
		ClientID:     getString("client_id"),
		Actor:        getString(actorField),
		Action:       getString(actionField),
		Resource:     getString(resourceField),
		Timestamp:    getString("timestamp"),
		SourceSystem: getString("source_system"),
	}
	if input.Actor == "" && fallbackActorField != "" {
		input.Actor = getString(fallbackActorField)
	}
	if value, ok := payload["authorization_context"].(map[string]interface{}); ok {
		input.AuthorizationContext = value
	}
	if value, ok := payload["metadata"].(map[string]interface{}); ok {
		input.Metadata = value
	}
	return input
}

// Normalize converts a decoded event into the canonical Agent audit shape.
func Normalize(input RawLogInput) (*models.AuditLog, error) {
	if input.Actor == "" || input.Action == "" || input.Resource == "" || input.SourceSystem == "" {
		return nil, errors.New("actor, action, resource, and source_system are required")
	}

	logID := input.LogID
	if logID == "" {
		var err error
		logID, err = newUUID()
		if err != nil {
			return nil, fmt.Errorf("generate log id: %w", err)
		}
	}

	timestamp := time.Now()
	if input.Timestamp != "" {
		if parsed, err := time.Parse(time.RFC3339, input.Timestamp); err == nil {
			timestamp = parsed
		}
	}

	authorizationContext, err := json.Marshal(input.AuthorizationContext)
	if err != nil {
		return nil, fmt.Errorf("marshal authorization_context: %w", err)
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal metadata: %w", err)
	}

	return &models.AuditLog{
		LogID:                logID,
		ClientID:             input.ClientID,
		Actor:                input.Actor,
		Action:               input.Action,
		Resource:             input.Resource,
		Timestamp:            timestamp,
		SourceSystem:         input.SourceSystem,
		AuthorizationContext: string(authorizationContext),
		Metadata:             string(metadata),
	}, nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
