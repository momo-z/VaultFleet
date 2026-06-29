package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/notify"
)

type NotificationHandler struct {
	DB              *db.Database
	notifierFactory notify.NotifierFactory
}

func NewNotificationHandler(database *db.Database) *NotificationHandler {
	return &NotificationHandler{
		DB:              database,
		notifierFactory: notify.NewNotifierFromConfig,
	}
}

type notificationRequest struct {
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
	Events []string       `json:"events"`
}

type updateNotificationRequest struct {
	Name   *string        `json:"name"`
	Type   *string        `json:"type"`
	Config map[string]any `json:"config"`
	Events []string       `json:"events"`
}

type notificationResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Config    map[string]any `json:"config"`
	Events    []string       `json:"events"`
	CreatedAt time.Time      `json:"created_at"`
}

func RegisterNotificationRoutes(rg *gin.RouterGroup, h *NotificationHandler) {
	rg.POST("/notifications", h.Create)
	rg.POST("/notifications/test", h.TestUnsaved)
	rg.GET("/notifications", h.List)
	rg.GET("/notifications/:id", h.Get)
	rg.PUT("/notifications/:id", h.Update)
	rg.DELETE("/notifications/:id", h.Delete)
	rg.POST("/notifications/:id/test", h.TestSend)
	rg.POST("/notifications/:id/test-config", h.TestDraft)
}

func (h *NotificationHandler) Create(c *gin.Context) {
	var request notificationRequest
	if err := bindStrictJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	configJSON, eventsJSON, ok := h.prepareNotificationInput(c, request.Type, request.Config, request.Events)
	if !ok {
		return
	}
	encryptedConfig, ok := h.encryptNotificationConfig(c, configJSON)
	if !ok {
		return
	}

	config := db.NotificationConfig{
		Name:   notificationName(request.Name, request.Type),
		Type:   request.Type,
		Config: encryptedConfig,
		Events: eventsJSON,
	}
	if err := h.DB.DB.Create(&config).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	h.writeNotificationDataResponse(c, http.StatusCreated, config)
}

func (h *NotificationHandler) List(c *gin.Context) {
	var configs []db.NotificationConfig
	if err := h.DB.DB.Order("created_at DESC").Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	responses := make([]notificationResponse, 0, len(configs))
	for _, config := range configs {
		response, err := h.newNotificationResponse(config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode notification config"})
			return
		}
		responses = append(responses, response)
	}

	writeDataResponse(c, http.StatusOK, responses)
}

func (h *NotificationHandler) Get(c *gin.Context) {
	config, ok := h.findNotificationByID(c, c.Param("id"))
	if !ok {
		return
	}

	h.writeNotificationResponse(c, http.StatusOK, config)
}

func (h *NotificationHandler) Update(c *gin.Context) {
	config, ok := h.findNotificationByID(c, c.Param("id"))
	if !ok {
		return
	}

	var request updateNotificationRequest
	if err := bindStrictJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	nextType := config.Type
	if request.Type != nil {
		nextType = *request.Type
	}

	currentConfig, ok := h.decryptNotificationConfigForRequest(c, config.Config)
	if !ok {
		return
	}

	nextConfig := currentConfig
	if request.Config != nil {
		mergedConfig, err := preserveNotificationRedactedSecrets(config.Type, currentConfig, request.Config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode notification config"})
			return
		}
		configJSON, ok := marshalNotificationConfig(c, request.Config)
		if mergedConfig != nil {
			configJSON, ok = marshalNotificationConfig(c, mergedConfig)
		}
		if !ok {
			return
		}
		nextConfig = configJSON
	}

	nextEvents := config.Events
	if request.Events != nil {
		eventsJSON, ok := marshalNotificationEvents(c, request.Events)
		if !ok {
			return
		}
		nextEvents = eventsJSON
	}

	if ok := validateNotificationConfig(c, nextType, json.RawMessage(nextConfig)); !ok {
		return
	}
	if ok := validateNotificationEvents(c, nextEvents); !ok {
		return
	}
	encryptedConfig, ok := h.encryptNotificationConfig(c, nextConfig)
	if !ok {
		return
	}

	config.Type = nextType
	if request.Name != nil {
		config.Name = notificationName(*request.Name, nextType)
	}
	config.Config = encryptedConfig
	config.Events = nextEvents
	if err := h.DB.DB.Save(&config).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	h.writeNotificationResponse(c, http.StatusOK, config)
}

func (h *NotificationHandler) Delete(c *gin.Context) {
	result := h.DB.DB.Delete(&db.NotificationConfig{}, "id = ?", c.Param("id"))
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "notification config not found"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *NotificationHandler) TestSend(c *gin.Context) {
	config, ok := h.findNotificationByID(c, c.Param("id"))
	if !ok {
		return
	}

	configJSON, ok := h.decryptNotificationConfigForRequest(c, config.Config)
	if !ok {
		return
	}

	notifier, err := h.newNotifier(config.Type, json.RawMessage(configJSON))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification config"})
		return
	}

	h.sendTestNotification(c, notifier)
}

func (h *NotificationHandler) TestUnsaved(c *gin.Context) {
	var request notificationRequest
	if err := bindStrictJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	configJSON, ok := marshalNotificationConfig(c, request.Config)
	if !ok {
		return
	}
	if ok := validateNotificationConfig(c, request.Type, json.RawMessage(configJSON)); !ok {
		return
	}

	notifier, err := h.newNotifier(request.Type, json.RawMessage(configJSON))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification config"})
		return
	}

	h.sendTestNotification(c, notifier)
}

func (h *NotificationHandler) TestDraft(c *gin.Context) {
	config, ok := h.findNotificationByID(c, c.Param("id"))
	if !ok {
		return
	}

	var request updateNotificationRequest
	if err := bindStrictJSON(c, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	nextType := config.Type
	if request.Type != nil {
		nextType = *request.Type
	}

	currentConfig, ok := h.decryptNotificationConfigForRequest(c, config.Config)
	if !ok {
		return
	}

	nextConfig := currentConfig
	if request.Config != nil {
		mergedConfig, err := preserveNotificationRedactedSecrets(config.Type, currentConfig, request.Config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decode notification config"})
			return
		}
		configJSON, ok := marshalNotificationConfig(c, request.Config)
		if mergedConfig != nil {
			configJSON, ok = marshalNotificationConfig(c, mergedConfig)
		}
		if !ok {
			return
		}
		nextConfig = configJSON
	}

	if ok := validateNotificationConfig(c, nextType, json.RawMessage(nextConfig)); !ok {
		return
	}

	notifier, err := h.newNotifier(nextType, json.RawMessage(nextConfig))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification config"})
		return
	}

	h.sendTestNotification(c, notifier)
}

func (h *NotificationHandler) sendTestNotification(c *gin.Context, notifier notify.Notifier) {
	if err := notifier.Send(c.Request.Context(), testNotifyMessage()); err != nil {
		if errors.Is(err, context.Canceled) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": "request cancelled"})
			return
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": "send notification failed", "detail": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *NotificationHandler) newNotifier(notificationType string, raw json.RawMessage) (notify.Notifier, error) {
	factory := h.notifierFactory
	if factory == nil {
		factory = notify.NewNotifierFromConfig
	}
	return factory(notificationType, raw)
}

func testNotifyMessage() notify.NotifyMessage {
	return notify.NotifyMessage{
		Title:     "Test Notification",
		Body:      "VaultFleet notification test message.",
		Level:     notify.LevelInfo,
		AgentName: "VaultFleet",
		Timestamp: time.Now().UTC(),
	}
}

func (h *NotificationHandler) findNotificationByID(c *gin.Context, id string) (db.NotificationConfig, bool) {
	var config db.NotificationConfig
	if err := h.DB.DB.First(&config, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "notification config not found"})
			return db.NotificationConfig{}, false
		}

		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return db.NotificationConfig{}, false
	}

	return config, true
}

func (h *NotificationHandler) prepareNotificationInput(c *gin.Context, notificationType string, config map[string]any, events []string) (string, string, bool) {
	configJSON, ok := marshalNotificationConfig(c, config)
	if !ok {
		return "", "", false
	}
	eventsJSON, ok := marshalNotificationEvents(c, events)
	if !ok {
		return "", "", false
	}
	if ok := validateNotificationConfig(c, notificationType, json.RawMessage(configJSON)); !ok {
		return "", "", false
	}
	if ok := validateNotificationEvents(c, eventsJSON); !ok {
		return "", "", false
	}
	return configJSON, eventsJSON, true
}

func (h *NotificationHandler) writeNotificationResponse(c *gin.Context, status int, config db.NotificationConfig) {
	response, err := h.newNotificationResponse(config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decode notification config"})
		return
	}

	c.JSON(status, response)
}

func (h *NotificationHandler) writeNotificationDataResponse(c *gin.Context, status int, config db.NotificationConfig) {
	response, err := h.newNotificationResponse(config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decode notification config"})
		return
	}

	writeDataResponse(c, status, response)
}

func (h *NotificationHandler) newNotificationResponse(config db.NotificationConfig) (notificationResponse, error) {
	configJSON, err := h.decryptNotificationConfig(config.Config)
	if err != nil {
		return notificationResponse{}, err
	}

	var rawConfig map[string]any
	if configJSON != "" {
		if err := json.Unmarshal([]byte(configJSON), &rawConfig); err != nil {
			return notificationResponse{}, err
		}
	}
	rawConfig = redactNotificationConfig(config.Type, rawConfig)

	var eventNames []string
	if config.Events != "" {
		if err := json.Unmarshal([]byte(config.Events), &eventNames); err != nil {
			return notificationResponse{}, err
		}
	}
	if eventNames == nil {
		eventNames = []string{}
	}

	return notificationResponse{
		ID:        config.ID,
		Name:      notificationName(config.Name, config.Type),
		Type:      config.Type,
		Config:    rawConfig,
		Events:    eventNames,
		CreatedAt: config.CreatedAt,
	}, nil
}

func notificationName(name string, notificationType string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	switch notificationType {
	case "telegram":
		return "Telegram"
	case "webhook":
		return "Webhook"
	case "email":
		return "Email"
	default:
		return notificationType
	}
}

func marshalNotificationConfig(c *gin.Context, value map[string]any) (string, bool) {
	if value == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return "", false
	}

	data, err := json.Marshal(value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return "", false
	}
	return string(data), true
}

func marshalNotificationEvents(c *gin.Context, events []string) (string, bool) {
	if len(events) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return "", false
	}

	data, err := json.Marshal(events)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return "", false
	}
	return string(data), true
}

func validateNotificationConfig(c *gin.Context, notificationType string, raw json.RawMessage) bool {
	if strings.TrimSpace(notificationType) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification type"})
		return false
	}
	if _, err := notify.NewNotifierFromConfig(notificationType, raw); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return false
	}
	return true
}

func validateNotificationEvents(c *gin.Context, rawEvents string) bool {
	var events []string
	if err := json.Unmarshal([]byte(rawEvents), &events); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid events"})
		return false
	}
	if len(events) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid events"})
		return false
	}
	for _, eventName := range events {
		switch eventName {
		case notify.EventBackupFailed, notify.EventAgentOffline:
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event"})
			return false
		}
	}
	return true
}

func (h *NotificationHandler) encryptNotificationConfig(c *gin.Context, plaintext string) (string, bool) {
	ciphertext, err := db.Encrypt(plaintext, h.DB.MasterKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encryption failed"})
		return "", false
	}
	return ciphertext, true
}

func (h *NotificationHandler) decryptNotificationConfigForRequest(c *gin.Context, ciphertext string) (string, bool) {
	plaintext, err := h.decryptNotificationConfig(ciphertext)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt notification config"})
		return "", false
	}
	return plaintext, true
}

func (h *NotificationHandler) decryptNotificationConfig(ciphertext string) (string, error) {
	plaintext, err := db.Decrypt(ciphertext, h.DB.MasterKey)
	if err == nil {
		return plaintext, nil
	}
	if json.Valid([]byte(ciphertext)) {
		return ciphertext, nil
	}
	return "", err
}

func redactNotificationConfig(notificationType string, config map[string]any) map[string]any {
	if config == nil {
		return nil
	}

	redacted := cloneJSONMap(config)
	switch notificationType {
	case "telegram":
		if _, ok := redacted["bot_token"]; ok {
			redacted["bot_token"] = redactedSecretValue
		}
	case "webhook":
		if _, ok := redacted["url"]; ok {
			redacted["url"] = redactedSecretValue
		}
		headers, ok := redacted["headers"].(map[string]any)
		if !ok {
			return redacted
		}
		for key := range headers {
			if isNotificationSecretHeader(key) {
				headers[key] = redactedSecretValue
			}
		}
	case "email":
		if _, ok := redacted["smtp_password"]; ok {
			redacted["smtp_password"] = redactedSecretValue
		}
	}
	return redacted
}

func preserveNotificationRedactedSecrets(notificationType string, currentJSON string, nextConfig map[string]any) (map[string]any, error) {
	if nextConfig == nil {
		return nil, nil
	}

	var currentConfig map[string]any
	if err := json.Unmarshal([]byte(currentJSON), &currentConfig); err != nil {
		return nil, err
	}

	merged := cloneJSONMap(nextConfig)
	switch notificationType {
	case "telegram":
		if merged["bot_token"] == redactedSecretValue {
			if currentValue, ok := currentConfig["bot_token"]; ok {
				merged["bot_token"] = currentValue
			}
		}
	case "webhook":
		if merged["url"] == redactedSecretValue {
			if currentValue, ok := currentConfig["url"]; ok {
				merged["url"] = currentValue
			}
		}
		currentHeaders, _ := currentConfig["headers"].(map[string]any)
		nextHeaders, _ := merged["headers"].(map[string]any)
		for key, value := range nextHeaders {
			if value == redactedSecretValue && isNotificationSecretHeader(key) {
				if currentValue, ok := currentHeaders[key]; ok {
					nextHeaders[key] = currentValue
				}
			}
		}
	case "email":
		if merged["smtp_password"] == redactedSecretValue {
			if currentValue, ok := currentConfig["smtp_password"]; ok {
				merged["smtp_password"] = currentValue
			}
		}
	}
	return merged, nil
}

func cloneJSONMap(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, item := range value {
		if nested, ok := item.(map[string]any); ok {
			clone[key] = cloneJSONMap(nested)
			continue
		}
		clone[key] = item
	}
	return clone
}

func isNotificationSecretHeader(name string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(name, "_", "-"))
	switch normalized {
	case "authorization", "proxy-authorization", "x-api-key", "x-auth-token", "x-access-token", "x-webhook-secret", "x-signature", "x-hub-signature", "x-hub-signature-256":
		return true
	default:
		return strings.Contains(normalized, "token") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "api-key")
	}
}

func bindStrictJSON(c *gin.Context, target any) error {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
