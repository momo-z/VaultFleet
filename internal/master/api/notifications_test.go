package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/notify"
)

type notificationAPISetup struct {
	database *db.Database
	handler  *NotificationHandler
	router   *gin.Engine
}

func setupNotificationAPI(t *testing.T) notificationAPISetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	handler := NewNotificationHandler(database)
	router := gin.New()
	RegisterNotificationRoutes(router.Group("/api"), handler)

	return notificationAPISetup{
		database: database,
		handler:  handler,
		router:   router,
	}
}

func TestCreateNotificationConfig(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := postAnyJSON(t, setup.router, "/api/notifications", map[string]any{
		"name": "Ops Webhook",
		"type": "webhook",
		"config": map[string]any{
			"url": "https://hooks.example.test/notify",
			"headers": map[string]any{
				"Authorization": "Bearer secret",
				"X-Trace":       "trace-id",
			},
		},
		"events": []string{"backup_failed", "agent_offline"},
	})

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	envelope := parseJSON(t, w)
	assert.Equal(t, true, envelope["ok"])
	body := requireMap(t, envelope["data"])
	assert.NotEmpty(t, body["id"])
	assert.Equal(t, "Ops Webhook", body["name"])
	assert.Equal(t, "webhook", body["type"])
	assertJSONList(t, body["events"], []string{"backup_failed", "agent_offline"})
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["url"])
	headers := requireMap(t, config["headers"])
	assert.Equal(t, redactedSecretValue, headers["Authorization"])
	assert.Equal(t, "trace-id", headers["X-Trace"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", body["id"]).Error)
	assert.Equal(t, "Ops Webhook", notificationStoredName(t, setup.database, stored.ID))
	assert.JSONEq(t, `["backup_failed","agent_offline"]`, stored.Events)
	assert.NotContains(t, stored.Config, "Bearer secret")
	assert.NotContains(t, stored.Config, "hooks.example.test")

	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://hooks.example.test/notify","headers":{"Authorization":"Bearer secret","X-Trace":"trace-id"}}`, plaintext)
}

func TestTelegramNotificationConfigEncryptsAndRedactsBotToken(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := postAnyJSON(t, setup.router, "/api/notifications", map[string]any{
		"type": "telegram",
		"config": map[string]any{
			"bot_token": "telegram-secret-token",
			"chat_id":   "chat-1",
			"base_url":  "https://api.example.test",
		},
		"events": []string{"backup_failed"},
	})

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	envelope := parseJSON(t, w)
	assert.Equal(t, true, envelope["ok"])
	body := requireMap(t, envelope["data"])
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["bot_token"])
	assert.Equal(t, "chat-1", config["chat_id"])
	assert.Equal(t, "https://api.example.test", config["base_url"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", body["id"]).Error)
	assert.NotContains(t, stored.Config, "telegram-secret-token")
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bot_token":"telegram-secret-token","chat_id":"chat-1","base_url":"https://api.example.test"}`, plaintext)
}

func TestCreateNotificationConfigValidatesTypeAndRequiredConfig(t *testing.T) {
	setup := setupNotificationAPI(t)

	tests := []struct {
		name string
		body map[string]any
	}{
		{
			name: "unknown type",
			body: map[string]any{
				"type":   "sms",
				"config": map[string]any{},
				"events": []string{"backup_failed"},
			},
		},
		{
			name: "telegram missing chat id",
			body: map[string]any{
				"type":   "telegram",
				"config": map[string]any{"bot_token": "token"},
				"events": []string{"backup_failed"},
			},
		},
		{
			name: "webhook missing url",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"headers": map[string]any{"X-Test": "value"}},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "webhook invalid scheme",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "ftp://hooks.example.test"},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "webhook nested header",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "https://hooks.example.test", "headers": map[string]any{"X-Nested": map[string]any{"value": "bad"}}},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "webhook numeric header",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "https://hooks.example.test", "headers": map[string]any{"X-Count": 3}},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "webhook invalid header name",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "https://hooks.example.test", "headers": map[string]any{"bad header": "value"}},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "webhook crlf header value",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "https://hooks.example.test", "headers": map[string]any{"X-Test": "bad\r\nvalue"}},
				"events": []string{"agent_offline"},
			},
		},
		{
			name: "events missing",
			body: map[string]any{
				"type":   "webhook",
				"config": map[string]any{"url": "https://hooks.example.test"},
			},
		},
		{
			name: "unknown top-level field",
			body: map[string]any{
				"type":    "webhook",
				"config":  map[string]any{"url": "https://hooks.example.test"},
				"events":  []string{"agent_offline"},
				"enabled": true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := postAnyJSON(t, setup.router, "/api/notifications", tt.body)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
		})
	}
}

func TestEmailNotificationConfigEncryptsAndRedactsSMTPPassword(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := postAnyJSON(t, setup.router, "/api/notifications", map[string]any{
		"name": "Ops Email",
		"type": "email",
		"config": map[string]any{
			"smtp_host":        "smtp.example.test",
			"smtp_port":        587,
			"smtp_security":    "starttls",
			"smtp_username":    "ops@example.test",
			"smtp_password":    "smtp-secret",
			"from":             "ops@example.test",
			"from_name":        "VaultFleet",
			"to":               []string{"admin@example.test"},
			"cc":               []string{"cc@example.test"},
			"subject_template": "[VaultFleet] {{.Title}}",
			"body_template":    "{{.Body}}",
			"body_format":      "text",
		},
		"events": []string{"backup_failed"},
	})

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	envelope := parseJSON(t, w)
	assert.Equal(t, true, envelope["ok"])
	body := requireMap(t, envelope["data"])
	assert.Equal(t, "Ops Email", body["name"])
	assert.Equal(t, "email", body["type"])
	config := requireMap(t, body["config"])
	assert.Equal(t, "smtp.example.test", config["smtp_host"])
	assert.Equal(t, float64(587), config["smtp_port"])
	assert.Equal(t, redactedSecretValue, config["smtp_password"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", body["id"]).Error)
	assert.NotContains(t, stored.Config, "smtp-secret")
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, `"smtp_password":"smtp-secret"`)
}

func TestListAndGetNotificationConfigsReturnEventArrays(t *testing.T) {
	setup := setupNotificationAPI(t)
	first := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{"url": "https://first.example.test"}, []string{"backup_failed"})
	second := createNotificationConfigViaAPI(t, setup.router, "telegram", map[string]any{"bot_token": "token", "chat_id": "chat"}, []string{"agent_offline"})

	w := getJSON(t, setup.router, "/api/notifications")
	require.Equal(t, http.StatusOK, w.Code)
	envelope := parseJSON(t, w)
	assert.Equal(t, true, envelope["ok"])
	listData := requireList(t, envelope["data"])
	list := make([]map[string]any, 0, len(listData))
	for _, item := range listData {
		list = append(list, requireMap(t, item))
	}
	require.Len(t, list, 2)
	seen := map[string]map[string]any{}
	for _, item := range list {
		seen[item["id"].(string)] = item
	}
	assert.NotEmpty(t, seen[first["id"].(string)]["name"])
	assert.NotEmpty(t, seen[second["id"].(string)]["name"])
	assertJSONList(t, seen[first["id"].(string)]["events"], []string{"backup_failed"})
	assertJSONList(t, seen[second["id"].(string)]["events"], []string{"agent_offline"})
	firstListConfig := requireMap(t, seen[first["id"].(string)]["config"])
	assert.Equal(t, redactedSecretValue, firstListConfig["url"])
	secondConfig := requireMap(t, seen[second["id"].(string)]["config"])
	assert.Equal(t, redactedSecretValue, secondConfig["bot_token"])
	assert.Equal(t, "chat", secondConfig["chat_id"])

	w = getJSON(t, setup.router, "/api/notifications/"+first["id"].(string))
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, first["id"], body["id"])
	assert.Equal(t, "webhook", body["type"])
	assertJSONList(t, body["events"], []string{"backup_failed"})
	firstConfig := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, firstConfig["url"])

	w = getJSON(t, setup.router, "/api/notifications/"+second["id"].(string))
	require.Equal(t, http.StatusOK, w.Code)
	body = parseJSON(t, w)
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["bot_token"])
	assert.Equal(t, "chat", config["chat_id"])
}

func TestGetNotificationConfigNotFound(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := getJSON(t, setup.router, "/api/notifications/missing")

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateNotificationConfig(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{"url": "https://old.example.test"}, []string{"backup_failed"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/notifications/"+id, map[string]any{
		"name": "Telegram Ops",
		"type": "telegram",
		"config": map[string]any{
			"bot_token": "new-token",
			"chat_id":   "new-chat",
		},
		"events": []string{"agent_offline"},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, id, body["id"])
	assert.Equal(t, "Telegram Ops", body["name"])
	assert.Equal(t, "telegram", body["type"])
	assertJSONList(t, body["events"], []string{"agent_offline"})
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["bot_token"])
	assert.Equal(t, "new-chat", config["chat_id"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	assert.Equal(t, "Telegram Ops", notificationStoredName(t, setup.database, stored.ID))
	assert.Equal(t, "telegram", stored.Type)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bot_token":"new-token","chat_id":"new-chat"}`, plaintext)
	assert.JSONEq(t, `["agent_offline"]`, stored.Events)
}

func TestUpdateNotificationConfigCanUpdateOnlyEvents(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{
		"url": "https://hooks.example.test",
		"headers": map[string]any{
			"Authorization": "Bearer old-secret",
			"X-Trace":       "trace-id",
		},
	}, []string{"backup_failed"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/notifications/"+id, map[string]any{
		"events": []string{"backup_failed", "agent_offline"},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, "webhook", body["type"])
	assertJSONList(t, body["events"], []string{"backup_failed", "agent_offline"})
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["url"])
	headers := requireMap(t, config["headers"])
	assert.Equal(t, redactedSecretValue, headers["Authorization"])
	assert.Equal(t, "trace-id", headers["X-Trace"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://hooks.example.test","headers":{"Authorization":"Bearer old-secret","X-Trace":"trace-id"}}`, plaintext)
}

func TestUpdateNotificationConfigPreservesRedactedSecrets(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "telegram", map[string]any{
		"bot_token": "old-token",
		"chat_id":   "chat-1",
	}, []string{"backup_failed"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/notifications/"+id, map[string]any{
		"config": map[string]any{
			"bot_token": redactedSecretValue,
			"chat_id":   "chat-2",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["bot_token"])
	assert.Equal(t, "chat-2", config["chat_id"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bot_token":"old-token","chat_id":"chat-2"}`, plaintext)
}

func TestUpdateWebhookNotificationConfigPreservesRedactedURLAndSecrets(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{
		"url": "https://hooks.example.test/secret-path?token=abc123",
		"headers": map[string]any{
			"Authorization": "Bearer old-secret",
			"X-Trace":       "old-trace",
		},
	}, []string{"backup_failed"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/notifications/"+id, map[string]any{
		"config": map[string]any{
			"url": redactedSecretValue,
			"headers": map[string]any{
				"Authorization": redactedSecretValue,
				"X-Trace":       "new-trace",
			},
		},
		"events": []string{"agent_offline"},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["url"])
	headers := requireMap(t, config["headers"])
	assert.Equal(t, redactedSecretValue, headers["Authorization"])
	assert.Equal(t, "new-trace", headers["X-Trace"])
	assertJSONList(t, body["events"], []string{"agent_offline"})

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.JSONEq(t, `{"url":"https://hooks.example.test/secret-path?token=abc123","headers":{"Authorization":"Bearer old-secret","X-Trace":"new-trace"}}`, plaintext)
}

func TestUpdateEmailNotificationConfigPreservesRedactedSMTPPassword(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "email", map[string]any{
		"smtp_host":        "smtp.example.test",
		"smtp_port":        587,
		"smtp_security":    "starttls",
		"smtp_username":    "ops@example.test",
		"smtp_password":    "old-secret",
		"from":             "ops@example.test",
		"to":               []string{"admin@example.test"},
		"subject_template": "{{.Title}}",
		"body_template":    "{{.Body}}",
		"body_format":      "text",
	}, []string{"backup_failed"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/notifications/"+id, map[string]any{
		"config": map[string]any{
			"smtp_host":        "smtp2.example.test",
			"smtp_port":        465,
			"smtp_security":    "tls",
			"smtp_username":    "ops@example.test",
			"smtp_password":    redactedSecretValue,
			"from":             "ops@example.test",
			"to":               []string{"admin@example.test", "backup@example.test"},
			"subject_template": "{{.Title}}",
			"body_template":    "{{.Body}}",
			"body_format":      "text",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	config := requireMap(t, body["config"])
	assert.Equal(t, redactedSecretValue, config["smtp_password"])
	assert.Equal(t, "smtp2.example.test", config["smtp_host"])

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, `"smtp_password":"old-secret"`)
	assert.Contains(t, plaintext, `"smtp_host":"smtp2.example.test"`)
}

func TestUpdateNotificationConfigValidationAndNotFound(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{"url": "https://hooks.example.test"}, []string{"backup_failed"})

	w := putJSON(t, setup.router, "/api/notifications/"+created["id"].(string), map[string]any{
		"type":   "webhook",
		"config": map[string]any{},
	})
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = putJSON(t, setup.router, "/api/notifications/"+created["id"].(string), map[string]any{
		"events":  []string{"agent_offline"},
		"enabled": true,
	})
	require.Equal(t, http.StatusBadRequest, w.Code)

	w = putJSON(t, setup.router, "/api/notifications/missing", map[string]any{"events": []string{"agent_offline"}})
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteNotificationConfig(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{"url": "https://hooks.example.test"}, []string{"backup_failed"})
	id := created["id"].(string)

	w := deleteJSON(t, setup.router, "/api/notifications/"+id)
	require.Equal(t, http.StatusNoContent, w.Code)

	w = getJSON(t, setup.router, "/api/notifications/"+id)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteNotificationConfigNotFound(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := deleteJSON(t, setup.router, "/api/notifications/missing")

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestTestNotificationConfigSendsSampleNotification(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{
		"url":     "https://hooks.example.test/secret-path?token=abc123",
		"headers": map[string]any{"Authorization": "Bearer secret"},
	}, []string{"backup_failed"})
	recorder := &apiRecordingNotifier{}
	setup.handler.notifierFactory = func(notificationType string, raw json.RawMessage) (notify.Notifier, error) {
		assert.Equal(t, "webhook", notificationType)
		assert.JSONEq(t, `{"url":"https://hooks.example.test/secret-path?token=abc123","headers":{"Authorization":"Bearer secret"}}`, string(raw))
		return recorder, nil
	}

	w := postAnyJSON(t, setup.router, "/api/notifications/"+created["id"].(string)+"/test", map[string]any{})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	require.Len(t, recorder.sent, 1)
	msg := recorder.sent[0]
	assert.Equal(t, "Test Notification", msg.Title)
	assert.Equal(t, notify.LevelInfo, msg.Level)
	assert.Equal(t, "VaultFleet", msg.AgentName)
	assert.NotEmpty(t, msg.Body)
}

func TestTestUnsavedNotificationConfigSendsSampleAndDoesNotPersist(t *testing.T) {
	setup := setupNotificationAPI(t)
	recorder := &apiRecordingNotifier{}
	setup.handler.notifierFactory = func(notificationType string, raw json.RawMessage) (notify.Notifier, error) {
		assert.Equal(t, "email", notificationType)
		assert.JSONEq(t, `{"smtp_host":"smtp.example.test","smtp_port":587,"smtp_security":"starttls","smtp_username":"ops@example.test","smtp_password":"draft-secret","from":"ops@example.test","to":["admin@example.test"],"subject_template":"{{.Title}}","body_template":"{{.Body}}","body_format":"text"}`, string(raw))
		return recorder, nil
	}

	w := postAnyJSON(t, setup.router, "/api/notifications/test", map[string]any{
		"type":   "email",
		"config": validEmailNotificationConfig("smtp.example.test", "draft-secret"),
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	require.Len(t, recorder.sent, 1)
	assert.Equal(t, "Test Notification", recorder.sent[0].Title)

	var count int64
	require.NoError(t, setup.database.DB.Model(&db.NotificationConfig{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestTestDraftNotificationConfigPreservesRedactedEmailPasswordAndDoesNotPersist(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "email", validEmailNotificationConfig("smtp.example.test", "old-secret"), []string{"backup_failed"})
	id := created["id"].(string)
	recorder := &apiRecordingNotifier{}
	setup.handler.notifierFactory = func(notificationType string, raw json.RawMessage) (notify.Notifier, error) {
		assert.Equal(t, "email", notificationType)
		assert.JSONEq(t, `{"smtp_host":"smtp2.example.test","smtp_port":465,"smtp_security":"tls","smtp_username":"ops@example.test","smtp_password":"old-secret","from":"ops@example.test","to":["admin@example.test"],"subject_template":"{{.Title}}","body_template":"{{.Body}}","body_format":"text"}`, string(raw))
		return recorder, nil
	}

	w := postAnyJSON(t, setup.router, "/api/notifications/"+id+"/test-config", map[string]any{
		"type": "email",
		"config": map[string]any{
			"smtp_host":        "smtp2.example.test",
			"smtp_port":        465,
			"smtp_security":    "tls",
			"smtp_username":    "ops@example.test",
			"smtp_password":    redactedSecretValue,
			"from":             "ops@example.test",
			"to":               []string{"admin@example.test"},
			"subject_template": "{{.Title}}",
			"body_template":    "{{.Body}}",
			"body_format":      "text",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, recorder.sent, 1)

	var stored db.NotificationConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.Config, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, `"smtp_host":"smtp.example.test"`)
	assert.Contains(t, plaintext, `"smtp_password":"old-secret"`)
	assert.NotContains(t, plaintext, "smtp2.example.test")
}

func TestTestUnsavedNotificationConfigValidatesConfig(t *testing.T) {
	setup := setupNotificationAPI(t)

	w := postAnyJSON(t, setup.router, "/api/notifications/test", map[string]any{
		"type": "email",
		"config": map[string]any{
			"smtp_host": "smtp.example.test",
		},
	})

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestTestNotificationConfigReturnsSendAndNotFoundErrors(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "webhook", map[string]any{"url": "https://hooks.example.test"}, []string{"backup_failed"})
	setup.handler.notifierFactory = func(string, json.RawMessage) (notify.Notifier, error) {
		return &apiRecordingNotifier{err: assert.AnError}, nil
	}

	w := postAnyJSON(t, setup.router, "/api/notifications/"+created["id"].(string)+"/test", map[string]any{})
	require.Equal(t, http.StatusBadGateway, w.Code)

	w = postAnyJSON(t, setup.router, "/api/notifications/missing/test", map[string]any{})
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestTestNotificationConfigReturnsSanitizedSendDetail(t *testing.T) {
	setup := setupNotificationAPI(t)
	created := createNotificationConfigViaAPI(t, setup.router, "email", validEmailNotificationConfig("smtp.example.test", "smtp-secret"), []string{"backup_failed"})
	setup.handler.notifierFactory = func(string, json.RawMessage) (notify.Notifier, error) {
		return &apiRecordingNotifier{err: errors.New("connect smtp server: dial tcp smtp.example.test:587: connection refused")}, nil
	}

	w := postAnyJSON(t, setup.router, "/api/notifications/"+created["id"].(string)+"/test", map[string]any{})

	require.Equal(t, http.StatusBadGateway, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, "send notification failed", body["error"])
	assert.Contains(t, body["detail"], "connect smtp server")
	assert.NotContains(t, w.Body.String(), "smtp-secret")
}

func createNotificationConfigViaAPI(t *testing.T, router http.Handler, notificationType string, config map[string]any, events []string) map[string]any {
	t.Helper()

	w := postAnyJSON(t, router, "/api/notifications", map[string]any{
		"type":   notificationType,
		"config": config,
		"events": events,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	envelope := parseJSON(t, w)
	assert.Equal(t, true, envelope["ok"])
	return requireMap(t, envelope["data"])
}

func notificationStoredName(t *testing.T, database *db.Database, id string) string {
	t.Helper()

	var name string
	require.NoError(t, database.DB.Raw("SELECT name FROM notification_configs WHERE id = ?", id).Scan(&name).Error)
	return name
}

type apiRecordingNotifier struct {
	sent []notify.NotifyMessage
	err  error
}

func (n *apiRecordingNotifier) Send(_ context.Context, msg notify.NotifyMessage) error {
	n.sent = append(n.sent, msg)
	return n.err
}

func (n *apiRecordingNotifier) Type() string {
	return "recording"
}

func validEmailNotificationConfig(host string, password string) map[string]any {
	return map[string]any{
		"smtp_host":        host,
		"smtp_port":        587,
		"smtp_security":    "starttls",
		"smtp_username":    "ops@example.test",
		"smtp_password":    password,
		"from":             "ops@example.test",
		"to":               []string{"admin@example.test"},
		"subject_template": "{{.Title}}",
		"body_template":    "{{.Body}}",
		"body_format":      "text",
	}
}
