package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/internal/master/storagecheck"
)

type testConfigSetup struct {
	database *db.Database
	bus      *events.Bus
	router   *gin.Engine
}

type fakeStorageTester struct {
	result   storagecheck.Result
	requests []storagecheck.Request
}

func (t *fakeStorageTester) Test(_ context.Context, request storagecheck.Request) storagecheck.Result {
	t.requests = append(t.requests, request)
	return t.result
}

func setupTestConfigAPI(t *testing.T) testConfigSetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	bus := events.NewBus()
	handler := NewConfigHandler(database)
	handler.EventBus = bus
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	return testConfigSetup{
		database: database,
		bus:      bus,
		router:   router,
	}
}

func TestCreateStorageConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := postAnyJSON(t, setup.router, "/api/storage", map[string]any{
		"name":        "Cloudflare R2",
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"provider":          "Cloudflare",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
			"endpoint":          "https://example.r2.cloudflarestorage.com",
		},
	})

	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.NotEmpty(t, body["id"])
	assert.Equal(t, "Cloudflare R2", body["name"])
	assert.Equal(t, "s3", body["rclone_type"])
	config := requireMap(t, body["rclone_config"])
	assert.Equal(t, "Cloudflare", config["provider"])
	assert.Equal(t, redactedSecretValue, config["secret_access_key"])
	assert.Equal(t, redactedSecretValue, config["access_key_id"])
	assert.Equal(t, "https://example.r2.cloudflarestorage.com", config["endpoint"])

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", body["id"]).Error)
	assert.NotContains(t, stored.RcloneConfig, "SECRET456")
	assert.NotContains(t, stored.RcloneConfig, "AKID123")
	assert.NotEqual(t, "", stored.RcloneConfig)
}

func TestCreateStorageConfigRejectsNonStringRcloneConfigValue(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := postAnyJSON(t, setup.router, "/api/storage", map[string]any{
		"name":        "Cloudflare R2",
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"region": 123,
		},
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var count int64
	require.NoError(t, setup.database.DB.Model(&db.StorageConfig{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestCreateStorageConfigRejectsUnsafeRcloneConfig(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
	}{
		{
			name: "reserved type key",
			config: map[string]any{
				"type":     "sftp",
				"provider": "Cloudflare",
			},
		},
		{
			name: "unsafe key characters",
			config: map[string]any{
				"bad=key":  "value",
				"provider": "Cloudflare",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := setupTestConfigAPI(t)

			w := postAnyJSON(t, setup.router, "/api/storage", map[string]any{
				"name":          "Injected",
				"rclone_type":   "s3",
				"rclone_config": tt.config,
			})

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			body := parseJSON(t, w)
			assert.NotEmpty(t, body["error"])

			var count int64
			require.NoError(t, setup.database.DB.Model(&db.StorageConfig{}).Count(&count).Error)
			assert.Equal(t, int64(0), count)
		})
	}
}

func TestStorageResponsesRedactSecretFields(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"provider":          "Cloudflare",
		"access_key_id":     "AKID123",
		"secret_access_key": "SECRET456",
		"secret":            "generic-secret",
		"password":          "pw",
		"pass":              "pass-value",
		"token":             "token-value",
		"client_secret":     "client-secret-value",
		"api_key":           "api-key-value",
		"private_key":       "private-key-value",
		"key_pem":           "key-pem-value",
		"custom_key":        "custom-key-value",
		"endpoint":          "https://example.r2.cloudflarestorage.com",
	})
	id := created["id"].(string)

	w := getJSON(t, setup.router, "/api/storage/"+id)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	config := requireMap(t, body["rclone_config"])
	assert.Equal(t, "Cloudflare", config["provider"])
	assert.Equal(t, "https://example.r2.cloudflarestorage.com", config["endpoint"])
	for _, key := range []string{"access_key_id", "secret_access_key", "secret", "password", "pass", "token", "client_secret", "api_key", "private_key", "key_pem", "custom_key"} {
		assert.Equal(t, redactedSecretValue, config[key], key)
	}

	w = getJSON(t, setup.router, "/api/storage")
	require.Equal(t, http.StatusOK, w.Code)
	body = parseJSON(t, w)
	list := requireList(t, body["data"])
	require.Len(t, list, 1)
	listConfig := requireMap(t, requireMap(t, list[0])["rclone_config"])
	assert.Equal(t, redactedSecretValue, listConfig["secret_access_key"])
	assert.Equal(t, redactedSecretValue, listConfig["api_key"])
	assert.Equal(t, redactedSecretValue, listConfig["private_key"])
	assert.Equal(t, redactedSecretValue, listConfig["key_pem"])
	assert.Equal(t, redactedSecretValue, listConfig["custom_key"])
	assert.Equal(t, "Cloudflare", listConfig["provider"])

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	assert.NotContains(t, stored.RcloneConfig, "SECRET456")
	assert.NotContains(t, stored.RcloneConfig, "AKID123")

	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "SECRET456")
	assert.Contains(t, plaintext, "AKID123")
}

func TestStorageTestUnsavedConfigDoesNotPersistAndReturnsResult(t *testing.T) {
	setup := setupTestConfigAPI(t)
	tester := &fakeStorageTester{
		result: storagecheck.Result{
			OK:        true,
			LatencyMs: 12,
			CheckedAt: time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = tester
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	w := postAnyJSON(t, router, "/api/storage/test", map[string]any{
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"provider":          "Cloudflare",
			"secret_access_key": "SECRET456",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	data := requireMap(t, body["data"])
	assert.Equal(t, true, data["ok"])
	assert.Equal(t, float64(12), data["latency_ms"])

	var count int64
	require.NoError(t, setup.database.DB.Model(&db.StorageConfig{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)

	require.Len(t, tester.requests, 1)
	assert.Equal(t, "s3", tester.requests[0].RcloneType)
	assert.Equal(t, "SECRET456", tester.requests[0].RcloneConfig["secret_access_key"])
}

func TestStorageTestFailureKeepsEnvelopeOK(t *testing.T) {
	setup := setupTestConfigAPI(t)
	tester := &fakeStorageTester{
		result: storagecheck.Result{
			OK:    false,
			Error: "failed",
		},
	}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = tester
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	w := postAnyJSON(t, router, "/api/storage/test", map[string]any{
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"provider": "Cloudflare",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.Equal(t, false, data["ok"])
	assert.Equal(t, "failed", data["error"])
}

func TestStorageTestInvalidRequestUsesErrorEnvelope(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := postAnyJSON(t, setup.router, "/api/storage/test", map[string]any{
		"rclone_type": "s3",
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.NotEmpty(t, body["error"])
}

func TestStorageTestSavedConfigDecryptsConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	tester := &fakeStorageTester{
		result: storagecheck.Result{
			OK:        true,
			LatencyMs: 12,
			CheckedAt: time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC),
		},
	}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = tester
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"provider":          "Cloudflare",
		"secret_access_key": "SECRET456",
	})

	w := postAnyJSON(t, router, "/api/storage/"+created["id"].(string)+"/test", nil)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, tester.requests, 1)
	assert.Equal(t, "s3", tester.requests[0].RcloneType)
	assert.Equal(t, "SECRET456", tester.requests[0].RcloneConfig["secret_access_key"])
}

func TestStorageTestSavedMissingUsesErrorEnvelope(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := postAnyJSON(t, setup.router, "/api/storage/missing/test", nil)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.NotEmpty(t, body["error"])
}

func TestStorageTestRejectsNonStringConfigWithErrorEnvelope(t *testing.T) {
	setup := setupTestConfigAPI(t)
	tester := &fakeStorageTester{}
	handler := NewConfigHandler(setup.database)
	handler.StorageTester = tester
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	w := postAnyJSON(t, router, "/api/storage/test", map[string]any{
		"rclone_type": "s3",
		"rclone_config": map[string]any{
			"provider": "Cloudflare",
			"chunk":    12,
		},
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, false, body["ok"])
	assert.Equal(t, "rclone config values must be strings", body["error"])
	assert.Empty(t, tester.requests)
}

func TestStorageTestRejectsUnsafeUnsavedConfigWithErrorEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
	}{
		{
			name: "reserved type key",
			config: map[string]any{
				"type":     "sftp",
				"provider": "Cloudflare",
			},
		},
		{
			name: "unsafe key characters",
			config: map[string]any{
				"bad key":  "value",
				"provider": "Cloudflare",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := setupTestConfigAPI(t)
			tester := &fakeStorageTester{}
			handler := NewConfigHandler(setup.database)
			handler.StorageTester = tester
			router := gin.New()
			api := router.Group("/api")
			RegisterStorageRoutes(api, handler)

			w := postAnyJSON(t, router, "/api/storage/test", map[string]any{
				"rclone_type":   "s3",
				"rclone_config": tt.config,
			})

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			body := parseJSON(t, w)
			assert.Equal(t, false, body["ok"])
			assert.NotEmpty(t, body["error"])
			assert.Empty(t, tester.requests)
		})
	}
}

func TestStorageTestRejectsUnsafeSavedConfigWithErrorEnvelope(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]any
	}{
		{
			name: "reserved type key",
			config: map[string]any{
				"type":     "sftp",
				"provider": "Cloudflare",
			},
		},
		{
			name: "unsafe key characters",
			config: map[string]any{
				" type":    "value",
				"provider": "Cloudflare",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setup := setupTestConfigAPI(t)
			tester := &fakeStorageTester{}
			handler := NewConfigHandler(setup.database)
			handler.StorageTester = tester
			router := gin.New()
			api := router.Group("/api")
			RegisterStorageRoutes(api, handler)

			storage := db.StorageConfig{
				Name:         "Unsafe",
				RcloneType:   "s3",
				RcloneConfig: mustEncryptMap(t, setup.database, tt.config),
			}
			require.NoError(t, setup.database.DB.Create(&storage).Error)

			w := postAnyJSON(t, router, "/api/storage/"+storage.ID+"/test", nil)

			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			body := parseJSON(t, w)
			assert.Equal(t, false, body["ok"])
			assert.NotEmpty(t, body["error"])
			assert.Empty(t, tester.requests)
		})
	}
}

func TestCreateStorageConfigValidatesRequiredFields(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := postAnyJSON(t, setup.router, "/api/storage", map[string]any{
		"name":        "Missing config",
		"rclone_type": "s3",
	})

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListStorageConfigs(t *testing.T) {
	setup := setupTestConfigAPI(t)

	createStorageConfig(t, setup.router, "Storage A", map[string]any{"key": "a"})
	createStorageConfig(t, setup.router, "Storage B", map[string]any{"key": "b"})

	w := getJSON(t, setup.router, "/api/storage")

	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	list := requireList(t, body["data"])
	require.Len(t, list, 2)

	configsByName := map[string]map[string]any{}
	for _, item := range list {
		itemMap := requireMap(t, item)
		configsByName[itemMap["name"].(string)] = requireMap(t, itemMap["rclone_config"])
	}
	assert.Equal(t, "a", configsByName["Storage A"]["key"])
	assert.Equal(t, "b", configsByName["Storage B"]["key"])
}

func TestGetStorageConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Primary Storage", map[string]any{"secret": "value"})

	w := getJSON(t, setup.router, "/api/storage/"+created["id"].(string))

	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	assert.Equal(t, created["id"], body["id"])
	assert.Equal(t, "Primary Storage", body["name"])
	config := requireMap(t, body["rclone_config"])
	assert.Equal(t, redactedSecretValue, config["secret"])
}

func TestGetStorageConfigNotFound(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := getJSON(t, setup.router, "/api/storage/missing")

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestUpdateStorageConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Old Name", map[string]any{"secret": "old"})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/storage/"+id, map[string]any{
		"name":        "New Name",
		"rclone_type": "sftp",
		"rclone_config": map[string]any{
			"secret": "new",
			"host":   "backup.example.com",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, id, body["id"])
	assert.Equal(t, "New Name", body["name"])
	assert.Equal(t, "sftp", body["rclone_type"])
	config := requireMap(t, body["rclone_config"])
	assert.Equal(t, redactedSecretValue, config["secret"])
	assert.Equal(t, "backup.example.com", config["host"])

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	assert.NotContains(t, stored.RcloneConfig, "new")
	assert.NotContains(t, stored.RcloneConfig, "backup.example.com")
}

func TestUpdateStorageConfigRejectsNonStringRcloneConfigValue(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"region":   "old-region",
		"endpoint": "old.example.com",
	})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/storage/"+id, map[string]any{
		"rclone_config": map[string]any{
			"region":   123,
			"endpoint": "new.example.com",
		},
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "old-region")
	assert.Contains(t, plaintext, "old.example.com")
	assert.NotContains(t, plaintext, "new.example.com")
	assert.NotContains(t, plaintext, "123")
}

func TestUpdateStorageConfigRejectsUnsafeRcloneType(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"provider": "Cloudflare",
		"endpoint": "old.example.com",
	})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/storage/"+id, map[string]any{
		"rclone_type": "s3\nendpoint = injected",
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.NotEmpty(t, body["error"])

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	assert.Equal(t, "s3", stored.RcloneType)
	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "old.example.com")
	assert.NotContains(t, plaintext, "injected")
}

func TestUpdateStorageConfigRejectsUnsafeRcloneConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"provider": "Cloudflare",
		"endpoint": "old.example.com",
	})
	id := created["id"].(string)

	w := putJSON(t, setup.router, "/api/storage/"+id, map[string]any{
		"rclone_config": map[string]any{
			"provider": "Cloudflare",
			"bad=key":  "value",
			"endpoint": "new.example.com",
		},
	})

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.NotEmpty(t, body["error"])

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "old.example.com")
	assert.NotContains(t, plaintext, "new.example.com")
	assert.NotContains(t, plaintext, "bad=key")
}

func TestUpdateStorageConfigRoundTripPreservesRedactedSecrets(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "Cloudflare R2", map[string]any{
		"provider":          "Cloudflare",
		"access_key_id":     "AKID123",
		"secret_access_key": "SECRET456",
		"endpoint":          "https://old.example.com",
	})
	id := created["id"].(string)

	w := getJSON(t, setup.router, "/api/storage/"+id)
	require.Equal(t, http.StatusOK, w.Code)
	body := parseJSON(t, w)
	roundTrippedConfig := requireMap(t, body["rclone_config"])
	roundTrippedConfig["endpoint"] = "https://new.example.com"

	w = putJSON(t, setup.router, "/api/storage/"+id, map[string]any{
		"rclone_config": roundTrippedConfig,
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", id).Error)
	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "AKID123")
	assert.Contains(t, plaintext, "SECRET456")
	assert.Contains(t, plaintext, "https://new.example.com")
	assert.NotContains(t, plaintext, redactedSecretValue)
}

func TestUpdateStorageConfigCredentialsMarkReferencedPoliciesUnsyncedAndPublishEvents(t *testing.T) {
	setup := setupTestConfigAPI(t)
	agentA := createStorageTestAgent(t, setup.database, "Tokyo-1")
	agentB := createStorageTestAgent(t, setup.database, "Osaka-1")
	created := createStorageConfig(t, setup.router, "Shared Storage", map[string]any{"secret": "old", "endpoint": "old.example.com"})
	storageID := created["id"].(string)
	policyA := createStorageTestPolicy(t, setup.database, agentA.ID, storageID, true)
	policyB := createStorageTestPolicy(t, setup.database, agentB.ID, storageID, true)

	received := make(chan events.Event, 2)
	setup.bus.Subscribe(events.PolicyChanged, func(event events.Event) {
		received <- event
	})

	w := putJSON(t, setup.router, "/api/storage/"+storageID, map[string]any{
		"rclone_config": map[string]any{
			"secret":   "new",
			"endpoint": "new.example.com",
		},
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var updatedA db.BackupPolicy
	require.NoError(t, setup.database.DB.First(&updatedA, "id = ?", policyA.ID).Error)
	assert.False(t, updatedA.Synced)
	var updatedB db.BackupPolicy
	require.NoError(t, setup.database.DB.First(&updatedB, "id = ?", policyB.ID).Error)
	assert.False(t, updatedB.Synced)

	assertPolicyChangedEvent(t, received, agentA.ID, "storage_updated")
	assertPolicyChangedEvent(t, received, agentB.ID, "storage_updated")
	assertNoPolicyChangedEvent(t, received)
}

func TestUpdateStorageConfigRollsBackWhenPolicyUnsyncFails(t *testing.T) {
	setup := setupTestConfigAPI(t)
	agent := createStorageTestAgent(t, setup.database, "Tokyo-1")
	created := createStorageConfig(t, setup.router, "Shared Storage", map[string]any{
		"secret":   "old",
		"endpoint": "old.example.com",
	})
	storageID := created["id"].(string)
	policy := createStorageTestPolicy(t, setup.database, agent.ID, storageID, true)
	handler := NewConfigHandler(setup.database)
	handler.markReferencedPoliciesUnsyncedFunc = func(*gorm.DB, string) ([]string, error) {
		return nil, assert.AnError
	}

	var storage db.StorageConfig
	require.NoError(t, setup.database.DB.First(&storage, "id = ?", storageID).Error)
	storage.RcloneConfig = mustEncryptMap(t, setup.database, map[string]any{
		"secret":   "new",
		"endpoint": "new.example.com",
	})

	_, err := handler.saveStorageUpdate(storage, true)

	require.ErrorIs(t, err, assert.AnError)

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", storageID).Error)
	plaintext, err := db.Decrypt(stored.RcloneConfig, setup.database.MasterKey)
	require.NoError(t, err)
	assert.Contains(t, plaintext, "old.example.com")
	assert.Contains(t, plaintext, "old")
	assert.NotContains(t, plaintext, "new.example.com")

	var storedPolicy db.BackupPolicy
	require.NoError(t, setup.database.DB.First(&storedPolicy, "id = ?", policy.ID).Error)
	assert.True(t, storedPolicy.Synced)
}

func TestUpdateStorageConfigNameOnlyDoesNotMarkPoliciesUnsyncedOrPublishEvents(t *testing.T) {
	setup := setupTestConfigAPI(t)
	agent := createStorageTestAgent(t, setup.database, "Tokyo-1")
	created := createStorageConfig(t, setup.router, "Old Name", map[string]any{"secret": "old"})
	storageID := created["id"].(string)
	policy := createStorageTestPolicy(t, setup.database, agent.ID, storageID, true)

	received := make(chan events.Event, 1)
	setup.bus.Subscribe(events.PolicyChanged, func(event events.Event) {
		received <- event
	})

	w := putJSON(t, setup.router, "/api/storage/"+storageID, map[string]any{
		"name": "New Name",
	})

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var stored db.BackupPolicy
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.True(t, stored.Synced)
	assertNoPolicyChangedEvent(t, received)
}

func TestDeleteStorageConfigReferencedByPolicyReturnsConflict(t *testing.T) {
	setup := setupTestConfigAPI(t)
	agent := createStorageTestAgent(t, setup.database, "Tokyo-1")
	created := createStorageConfig(t, setup.router, "In Use", map[string]any{"key": "value"})
	storageID := created["id"].(string)
	createStorageTestPolicy(t, setup.database, agent.ID, storageID, true)

	w := deleteJSON(t, setup.router, "/api/storage/"+storageID)

	require.Equal(t, http.StatusConflict, w.Code)

	var stored db.StorageConfig
	require.NoError(t, setup.database.DB.First(&stored, "id = ?", storageID).Error)
}

func TestUpdateStorageConfigNotFound(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := putJSON(t, setup.router, "/api/storage/missing", map[string]any{"name": "Nope"})

	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteStorageConfig(t *testing.T) {
	setup := setupTestConfigAPI(t)
	created := createStorageConfig(t, setup.router, "To Delete", map[string]any{"key": "value"})
	id := created["id"].(string)

	w := deleteJSON(t, setup.router, "/api/storage/"+id)

	require.Equal(t, http.StatusNoContent, w.Code)

	w = getJSON(t, setup.router, "/api/storage/"+id)
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteStorageConfigNotFound(t *testing.T) {
	setup := setupTestConfigAPI(t)

	w := deleteJSON(t, setup.router, "/api/storage/missing")

	require.Equal(t, http.StatusNotFound, w.Code)
}

func createStorageConfig(t *testing.T, router http.Handler, name string, config map[string]any) map[string]any {
	t.Helper()

	w := postAnyJSON(t, router, "/api/storage", map[string]any{
		"name":          name,
		"rclone_type":   "s3",
		"rclone_config": config,
	})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	return parseJSON(t, w)
}

func parseJSONInto(t *testing.T, w *httptest.ResponseRecorder, target any) {
	t.Helper()

	require.NoError(t, json.Unmarshal(w.Body.Bytes(), target))
}

func postAnyJSON(t *testing.T, router http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func putJSON(t *testing.T, router http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func requireMap(t *testing.T, value any) map[string]any {
	t.Helper()

	result, ok := value.(map[string]any)
	require.True(t, ok, "expected map, got %T", value)
	return result
}

func requireList(t *testing.T, value any) []any {
	t.Helper()

	result, ok := value.([]any)
	require.True(t, ok, "expected list, got %T", value)
	return result
}

func createStorageTestAgent(t *testing.T, database *db.Database, name string) db.Agent {
	t.Helper()

	agent := db.Agent{
		Name:   name,
		Status: "online",
	}
	require.NoError(t, database.DB.Create(&agent).Error)
	return agent
}

func createStorageTestPolicy(t *testing.T, database *db.Database, agentID string, storageID string, synced bool) db.BackupPolicy {
	t.Helper()

	encryptedPassword, err := db.Encrypt("restic-password", database.MasterKey)
	require.NoError(t, err)

	policy := db.BackupPolicy{
		AgentID:         agentID,
		StorageID:       storageID,
		RepoPath:        "vaultfleet/" + agentID,
		ResticPassword:  encryptedPassword,
		BackupDirs:      `["/etc"]`,
		ExcludePatterns: `[]`,
		Schedule:        "0 3 * * *",
		Retention:       `{"keep_last":3}`,
		Synced:          synced,
	}
	require.NoError(t, database.DB.Create(&policy).Error)
	return policy
}

func mustEncryptMap(t *testing.T, database *db.Database, value map[string]any) string {
	t.Helper()

	plaintext, err := json.Marshal(value)
	require.NoError(t, err)
	ciphertext, err := db.Encrypt(string(plaintext), database.MasterKey)
	require.NoError(t, err)
	return ciphertext
}

func assertPolicyChangedEvent(t *testing.T, received <-chan events.Event, agentID string, action string) {
	t.Helper()

	select {
	case event := <-received:
		assert.Equal(t, events.PolicyChanged, event.Type)
		payload := requireMap(t, event.Payload)
		assert.Equal(t, agentID, payload["agent_id"])
		assert.Equal(t, action, payload["action"])
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for policy changed event for %s", agentID)
	}
}

func assertNoPolicyChangedEvent(t *testing.T, received <-chan events.Event) {
	t.Helper()

	select {
	case event := <-received:
		t.Fatalf("unexpected policy changed event: %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func TestListProviders(t *testing.T) {
	setup := setupTestConfigAPI(t)
	handler := NewConfigHandler(setup.database)
	handler.ProviderLoader = &storagecheck.ProviderLoader{
		RunFunc: func() ([]byte, error) {
			return []byte(`{
				"providers": [{
					"Name": "s3",
					"Options": [{
						"Name": "provider",
						"Examples": [
							{"Value": "AWS", "Help": "Amazon Web Services"},
							{"Value": "Minio", "Help": "Minio Object Storage"}
						]
					}]
				}]
			}`), nil
		},
	}
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	w := getJSON(t, router, "/api/storage/providers")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	list := requireList(t, body["data"])
	require.Len(t, list, 2)
	first := requireMap(t, list[0])
	assert.Equal(t, "AWS", first["value"])
	assert.Equal(t, "Amazon Web Services", first["help"])
	second := requireMap(t, list[1])
	assert.Equal(t, "Minio", second["value"])
}

func TestListProvidersRcloneUnavailableReturnsFallback(t *testing.T) {
	setup := setupTestConfigAPI(t)
	handler := NewConfigHandler(setup.database)
	handler.ProviderLoader = &storagecheck.ProviderLoader{
		RunFunc: func() ([]byte, error) {
			return nil, assert.AnError
		},
	}
	router := gin.New()
	api := router.Group("/api")
	RegisterStorageRoutes(api, handler)

	w := getJSON(t, router, "/api/storage/providers")

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	list := requireList(t, body["data"])
	require.NotEmpty(t, list)
	first := requireMap(t, list[0])
	assert.Equal(t, "AWS", first["value"])
}
