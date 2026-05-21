package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/internal/master/storagecheck"
)

type ConfigHandler struct {
	DB             *db.Database
	EventBus       *events.Bus
	MasterKey      []byte
	StorageTester  StorageTester
	ProviderLoader *storagecheck.ProviderLoader

	markReferencedPoliciesUnsyncedFunc func(*gorm.DB, string) ([]string, error)
}

type StorageTester interface {
	Test(ctx context.Context, request storagecheck.Request) storagecheck.Result
}

func NewConfigHandler(database *db.Database) *ConfigHandler {
	return &ConfigHandler{
		DB:            database,
		MasterKey:     database.MasterKey,
		StorageTester: storagecheck.NewService(nil),
	}
}

type createStorageRequest struct {
	Name         string         `json:"name" binding:"required"`
	RcloneType   string         `json:"rclone_type" binding:"required"`
	RcloneConfig map[string]any `json:"rclone_config" binding:"required"`
}

type updateStorageRequest struct {
	Name         string         `json:"name"`
	RcloneType   string         `json:"rclone_type"`
	RcloneConfig map[string]any `json:"rclone_config"`
}

type testStorageRequest struct {
	RcloneType   string         `json:"rclone_type" binding:"required"`
	RcloneConfig map[string]any `json:"rclone_config" binding:"required"`
}

type storageResponse struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	RcloneType   string         `json:"rclone_type"`
	RcloneConfig map[string]any `json:"rclone_config"`
	CreatedAt    time.Time      `json:"created_at"`
}

func RegisterStorageRoutes(rg *gin.RouterGroup, h *ConfigHandler) {
	rg.GET("/storage/providers", h.ListProviders)
	rg.POST("/storage", h.CreateStorage)
	rg.POST("/storage/test", h.TestUnsavedStorage)
	rg.GET("/storage", h.ListStorage)
	rg.GET("/storage/:id", h.GetStorage)
	rg.PUT("/storage/:id", h.UpdateStorage)
	rg.DELETE("/storage/:id", h.DeleteStorage)
	rg.POST("/storage/:id/test", h.TestSavedStorage)
}

func (h *ConfigHandler) CreateStorage(c *gin.Context) {
	var request createStorageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	config, ok := stringifyRcloneConfig(c, request.RcloneConfig)
	if !ok {
		return
	}
	if !validateStorageCheckRequest(c, request.RcloneType, config, writeBareErrorResponse) {
		return
	}

	encryptedConfig, ok := h.encryptStringMap(c, config)
	if !ok {
		return
	}

	storage := db.StorageConfig{
		Name:         request.Name,
		RcloneType:   request.RcloneType,
		RcloneConfig: encryptedConfig,
	}
	if err := h.DB.DB.Create(&storage).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	h.writeStorageResponse(c, http.StatusCreated, storage)
}

func (h *ConfigHandler) ListStorage(c *gin.Context) {
	var configs []db.StorageConfig
	if err := h.DB.DB.Order("created_at DESC").Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	responses := make([]storageResponse, 0, len(configs))
	for _, storage := range configs {
		response, err := h.newStorageResponse(storage)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt storage config"})
			return
		}
		responses = append(responses, response)
	}

	writeDataResponse(c, http.StatusOK, responses)
}

func (h *ConfigHandler) GetStorage(c *gin.Context) {
	storage, ok := h.findStorageByID(c, c.Param("id"))
	if !ok {
		return
	}

	h.writeStorageResponse(c, http.StatusOK, storage)
}

func (h *ConfigHandler) UpdateStorage(c *gin.Context) {
	storage, ok := h.findStorageByID(c, c.Param("id"))
	if !ok {
		return
	}
	configChanged := false

	var request updateStorageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	nextRcloneType := storage.RcloneType
	nextEncryptedConfig := storage.RcloneConfig
	if request.Name != "" {
		storage.Name = request.Name
	}
	if request.RcloneType != "" {
		nextRcloneType = request.RcloneType
		configChanged = true
	}
	if request.RcloneConfig != nil {
		if _, ok := stringifyRcloneConfig(c, request.RcloneConfig); !ok {
			return
		}

		nextConfig, err := h.preserveRedactedSecrets(storage.RcloneConfig, request.RcloneConfig)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt storage config"})
			return
		}

		finalConfig, ok := stringifyRcloneConfig(c, nextConfig)
		if !ok {
			return
		}
		if !validateStorageCheckRequest(c, nextRcloneType, finalConfig, writeBareErrorResponse) {
			return
		}

		encryptedConfig, ok := h.encryptStringMap(c, finalConfig)
		if !ok {
			return
		}
		nextEncryptedConfig = encryptedConfig
		configChanged = true
	} else if request.RcloneType != "" {
		currentConfig, err := h.decryptMap(storage.RcloneConfig)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt storage config"})
			return
		}

		finalConfig, ok := stringifyRcloneConfig(c, currentConfig)
		if !ok {
			return
		}
		if !validateStorageCheckRequest(c, nextRcloneType, finalConfig, writeBareErrorResponse) {
			return
		}
	}

	storage.RcloneType = nextRcloneType
	storage.RcloneConfig = nextEncryptedConfig

	agentIDs, err := h.saveStorageUpdate(storage, configChanged)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	h.publishStorageChanged(agentIDs)

	h.writeStorageResponse(c, http.StatusOK, storage)
}

func (h *ConfigHandler) DeleteStorage(c *gin.Context) {
	hasPolicies, ok := h.storageHasPolicies(c, c.Param("id"))
	if !ok {
		return
	}
	if hasPolicies {
		c.JSON(http.StatusConflict, gin.H{"error": "storage config is referenced by policies"})
		return
	}

	result := h.DB.DB.Delete(&db.StorageConfig{}, "id = ?", c.Param("id"))
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "storage config not found"})
		return
	}

	c.Status(http.StatusNoContent)
}

func (h *ConfigHandler) TestUnsavedStorage(c *gin.Context) {
	var request testStorageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, "invalid request")
		return
	}

	config, ok := stringifyRcloneConfigWithErrorWriter(c, request.RcloneConfig, writeErrorResponse)
	if !ok {
		return
	}
	if !validateStorageCheckRequest(c, request.RcloneType, config, writeErrorResponse) {
		return
	}

	result := h.storageTester().Test(c.Request.Context(), storagecheck.Request{
		RcloneType:   request.RcloneType,
		RcloneConfig: config,
	})
	writeStorageTestResult(c, http.StatusOK, result)
}

func (h *ConfigHandler) TestSavedStorage(c *gin.Context) {
	storage, ok := h.findStorageByIDWithErrorWriter(c, c.Param("id"), writeErrorResponse)
	if !ok {
		return
	}

	rawConfig, err := h.decryptMap(storage.RcloneConfig)
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "decrypt storage config")
		return
	}

	config, ok := stringifyRcloneConfigWithErrorWriter(c, rawConfig, writeErrorResponse)
	if !ok {
		return
	}
	if !validateStorageCheckRequest(c, storage.RcloneType, config, writeErrorResponse) {
		return
	}

	result := h.storageTester().Test(c.Request.Context(), storagecheck.Request{
		RcloneType:   storage.RcloneType,
		RcloneConfig: config,
	})
	writeStorageTestResult(c, http.StatusOK, result)
}

func (h *ConfigHandler) ListProviders(c *gin.Context) {
	if h.ProviderLoader == nil {
		writeDataResponse(c, http.StatusOK, []storagecheck.S3Provider{})
		return
	}
	providers, err := h.ProviderLoader.Load()
	if err != nil {
		writeDataResponse(c, http.StatusOK, []storagecheck.S3Provider{})
		return
	}
	writeDataResponse(c, http.StatusOK, providers)
}

func writeStorageTestResult(c *gin.Context, status int, result storagecheck.Result) {
	c.JSON(status, gin.H{
		"ok":   true,
		"data": result,
	})
}

func (h *ConfigHandler) storageHasPolicies(c *gin.Context, storageID string) (bool, bool) {
	var count int64
	if err := h.DB.DB.Model(&db.BackupPolicy{}).Where("storage_id = ?", storageID).Count(&count).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return false, false
	}

	return count > 0, true
}

func (h *ConfigHandler) saveStorageUpdate(storage db.StorageConfig, configChanged bool) ([]string, error) {
	var agentIDs []string

	err := h.DB.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&storage).Error; err != nil {
			return err
		}
		if !configChanged {
			return nil
		}

		mark := h.markReferencedPoliciesUnsynced
		if h.markReferencedPoliciesUnsyncedFunc != nil {
			mark = h.markReferencedPoliciesUnsyncedFunc
		}

		affectedAgentIDs, err := mark(tx, storage.ID)
		if err != nil {
			return err
		}
		agentIDs = affectedAgentIDs
		return nil
	})
	if err != nil {
		return nil, err
	}

	return agentIDs, nil
}

func (h *ConfigHandler) markReferencedPoliciesUnsynced(tx *gorm.DB, storageID string) ([]string, error) {
	var policies []db.BackupPolicy
	if err := tx.Where("storage_id = ?", storageID).Find(&policies).Error; err != nil {
		return nil, err
	}
	if len(policies) == 0 {
		return nil, nil
	}

	if err := tx.Model(&db.BackupPolicy{}).Where("storage_id = ?", storageID).Update("synced", false).Error; err != nil {
		return nil, err
	}

	seenAgents := make(map[string]bool, len(policies))
	agentIDs := make([]string, 0, len(policies))
	for _, policy := range policies {
		if seenAgents[policy.AgentID] {
			continue
		}
		seenAgents[policy.AgentID] = true
		agentIDs = append(agentIDs, policy.AgentID)
	}

	return agentIDs, nil
}

func (h *ConfigHandler) publishStorageChanged(agentIDs []string) {
	if h.EventBus == nil {
		return
	}

	for _, agentID := range agentIDs {
		h.EventBus.Publish(events.Event{
			Type: events.PolicyChanged,
			Payload: map[string]interface{}{
				"agent_id": agentID,
				"action":   "storage_updated",
			},
		})
	}
}

func (h *ConfigHandler) findStorageByID(c *gin.Context, id string) (db.StorageConfig, bool) {
	return h.findStorageByIDWithErrorWriter(c, id, writeBareErrorResponse)
}

func (h *ConfigHandler) findStorageByIDWithErrorWriter(c *gin.Context, id string, writeError func(*gin.Context, int, string)) (db.StorageConfig, bool) {
	var storage db.StorageConfig
	if err := h.DB.DB.First(&storage, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeError(c, http.StatusNotFound, "storage config not found")
			return db.StorageConfig{}, false
		}

		writeError(c, http.StatusInternalServerError, "database error")
		return db.StorageConfig{}, false
	}

	return storage, true
}

func (h *ConfigHandler) writeStorageResponse(c *gin.Context, status int, storage db.StorageConfig) {
	response, err := h.newStorageResponse(storage)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "decrypt storage config"})
		return
	}

	writeDataResponse(c, status, response)
}

func (h *ConfigHandler) newStorageResponse(storage db.StorageConfig) (storageResponse, error) {
	config, err := h.decryptMap(storage.RcloneConfig)
	if err != nil {
		return storageResponse{}, err
	}

	return storageResponse{
		ID:           storage.ID,
		Name:         storage.Name,
		RcloneType:   storage.RcloneType,
		RcloneConfig: redactRcloneConfig(config),
		CreatedAt:    storage.CreatedAt,
	}, nil
}

func (h *ConfigHandler) encryptMap(c *gin.Context, value map[string]any) (string, bool) {
	return h.encryptJSON(c, value)
}

func (h *ConfigHandler) encryptStringMap(c *gin.Context, value map[string]string) (string, bool) {
	return h.encryptJSON(c, value)
}

func (h *ConfigHandler) encryptJSON(c *gin.Context, value any) (string, bool) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return "", false
	}

	ciphertext, err := db.Encrypt(string(plaintext), h.MasterKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "encryption failed"})
		return "", false
	}

	return ciphertext, true
}

func (h *ConfigHandler) decryptMap(ciphertext string) (map[string]any, error) {
	plaintext, err := db.Decrypt(ciphertext, h.MasterKey)
	if err != nil {
		return nil, err
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(plaintext), &result); err != nil {
		return nil, err
	}

	return result, nil
}

func (h *ConfigHandler) preserveRedactedSecrets(currentCiphertext string, nextConfig map[string]any) (map[string]any, error) {
	currentConfig, err := h.decryptMap(currentCiphertext)
	if err != nil {
		return nil, err
	}

	merged := make(map[string]any, len(nextConfig))
	for key, value := range nextConfig {
		if isRcloneSecretKey(key) && value == redactedSecretValue {
			if currentValue, ok := currentConfig[key]; ok {
				merged[key] = currentValue
				continue
			}
		}
		merged[key] = value
	}

	return merged, nil
}

func (h *ConfigHandler) storageTester() StorageTester {
	if h.StorageTester != nil {
		return h.StorageTester
	}
	return storagecheck.NewService(nil)
}

func stringifyRcloneConfig(c *gin.Context, config map[string]any) (map[string]string, bool) {
	return stringifyRcloneConfigWithErrorWriter(c, config, writeBareErrorResponse)
}

func stringifyRcloneConfigWithErrorWriter(c *gin.Context, config map[string]any, writeError func(*gin.Context, int, string)) (map[string]string, bool) {
	result := make(map[string]string, len(config))
	for key, value := range config {
		stringValue, ok := value.(string)
		if !ok {
			writeError(c, http.StatusBadRequest, "rclone config values must be strings")
			return nil, false
		}
		result[key] = stringValue
	}
	return result, true
}

func validateStorageCheckRequest(c *gin.Context, rcloneType string, config map[string]string, writeError func(*gin.Context, int, string)) bool {
	if err := storagecheck.ValidateRequest(storagecheck.Request{
		RcloneType:   rcloneType,
		RcloneConfig: config,
	}); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func writeBareErrorResponse(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

const redactedSecretValue = "[redacted]"

func redactRcloneConfig(config map[string]any) map[string]any {
	redacted := make(map[string]any, len(config))
	for key, value := range config {
		if isRcloneSecretKey(key) {
			redacted[key] = redactedSecretValue
			continue
		}
		redacted[key] = value
	}

	return redacted
}

func isRcloneSecretKey(key string) bool {
	return storagecheck.IsSecretConfigKey(key)
}
