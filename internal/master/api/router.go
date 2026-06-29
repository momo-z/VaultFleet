package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/internal/master/logbuf"
	"vaultfleet/internal/master/storagecheck"
	"vaultfleet/pkg/protocol"
	"vaultfleet/pkg/rcloneobscure"
)

type RouterHub interface {
	BrowseHub
	SnapshotHub
	RestoreHub
	CommandHub
	AgentStatusProvider
}

type RouterConfig struct {
	Database           *db.Database
	Hub                RouterHub
	CommandService     *commands.Service
	EventBus           *events.Bus
	AgentWebSocket     gin.HandlerFunc
	TaskProgressGetter func(agentID string, messageID string) *protocol.BackupProgressPayload
	Version            string
	LogBuf             *logbuf.RingBuffer
}

type PolicyPushTracker struct {
	mu     sync.Mutex
	pushes map[string]trackedPolicyPush
}

type trackedPolicyPush struct {
	AgentID   string
	PolicyID  string
	UpdatedAt time.Time
}

type CurrentPolicyCommand struct {
	Message         *protocol.Message
	AgentID         string
	PolicyID        string
	StorageID       string
	PolicyUpdatedAt time.Time
}

type PolicyCommandCompleter interface {
	CompletePolicyAck(ctx context.Context, agentID string, messageID string, success bool, errorText string) error
}

func NewPolicyPushTracker() *PolicyPushTracker {
	return &PolicyPushTracker{
		pushes: make(map[string]trackedPolicyPush),
	}
}

func (t *PolicyPushTracker) Track(messageID string, agentID string, policyID string, updatedAt time.Time) {
	if t == nil || messageID == "" || agentID == "" || policyID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.pushes[messageID] = trackedPolicyPush{
		AgentID:   agentID,
		PolicyID:  policyID,
		UpdatedAt: updatedAt,
	}
}

func (t *PolicyPushTracker) Get(messageID string, agentID string) (trackedPolicyPush, bool) {
	if t == nil || messageID == "" || agentID == "" {
		return trackedPolicyPush{}, false
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	tracked, ok := t.pushes[messageID]
	if !ok || tracked.AgentID != agentID {
		return trackedPolicyPush{}, false
	}
	return tracked, true
}

func (t *PolicyPushTracker) Delete(messageID string) {
	if t == nil || messageID == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.pushes, messageID)
}

var defaultPolicyPushTracker = NewPolicyPushTracker()

func NewRouter(cfg RouterConfig) *gin.Engine {
	if cfg.Database == nil || cfg.Database.DB == nil {
		panic("router database is required")
	}

	r := gin.New()
	r.Use(gin.Recovery())

	commandService := cfg.CommandService
	if commandService == nil {
		commandService = commands.NewService(cfg.Database, cfg.Hub)
	}
	if commandService != nil && commandService.Hub == nil {
		commandService.Hub = cfg.Hub
	}

	authHandler := NewAuthHandler(cfg.Database)
	agentHandler := NewAgentHandler(cfg.Database)
	storageHandler := NewConfigHandler(cfg.Database)
	storageHandler.EventBus = cfg.EventBus
	storageHandler.ProviderLoader = storagecheck.NewProviderLoader()
	policyHandler := NewPolicyHandler(cfg.Database, cfg.EventBus)
	browseHandler := NewBrowseHandler(cfg.Database, cfg.Hub)
	snapshotHandler := NewSnapshotHandler(cfg.Database, cfg.Hub)
	snapshotHandler.Commands = commandService
	snapshotBrowseHandler := NewSnapshotBrowseHandler(cfg.Database, cfg.Hub)
	restoreHandler := NewRestoreHandler(cfg.Database, cfg.Hub)
	restoreHandler.Commands = commandService
	taskHandler := NewTaskHandler(cfg.Database, cfg.Hub)
	taskHandler.Commands = commandService
	taskHandler.ProgressGetter = cfg.TaskProgressGetter
	commandHandler := NewCommandHandler(cfg.Database)
	notificationHandler := NewNotificationHandler(cfg.Database)
	systemHandler := NewSystemHandler(cfg.Database)
	systemHandler.Version = cfg.Version
	diagnosticHandler := NewDiagnosticHandler(cfg.Database, cfg.Hub, cfg.LogBuf)
	diagnosticHandler.Version = cfg.Version
	healthHandler := NewHealthHandler(cfg.Database, cfg.Hub)

	public := r.Group("/api")
	public.GET("/auth/check", authHandler.CheckInit)
	public.POST("/auth/init", authHandler.InitSetup)
	public.POST("/auth/login", authHandler.Login)
	public.POST("/agent/enroll", agentHandler.Enroll)

	agentWebSocket := cfg.AgentWebSocket
	if agentWebSocket == nil {
		agentWebSocket = unavailableAgentWebSocket(cfg.Database)
	}
	r.GET("/ws/agent", agentWebSocket)

	protected := r.Group("/api")
	protected.Use(RequireInit(cfg.Database), RequireAuth(authHandler.Sessions))
	protected.POST("/auth/logout", authHandler.Logout)
	protected.POST("/agents", agentHandler.Create)
	protected.GET("/agents", agentHandler.List)
	protected.GET("/agents/:id", agentHandler.Get)
	protected.DELETE("/agents/:id", agentHandler.Delete)
	protected.POST("/agents/:id/regenerate-token", agentHandler.RegenerateToken)
	protected.GET("/agents/:id/install-token", agentHandler.GetInstallToken)
	RegisterStorageRoutes(protected, storageHandler)
	RegisterPolicyRoutes(protected, policyHandler)
	RegisterBrowseRoutes(protected, browseHandler)
	RegisterSnapshotRoutes(protected, snapshotHandler)
	RegisterSnapshotBrowseRoutes(protected, snapshotBrowseHandler)
	RegisterRestoreRoutes(protected, restoreHandler)
	RegisterTaskRoutes(protected, taskHandler)
	RegisterCommandRoutes(protected, commandHandler)
	RegisterNotificationRoutes(protected, notificationHandler)
	RegisterSystemRoutes(protected.Group("/system"), systemHandler)
	RegisterDiagnosticRoutes(protected.Group("/system"), diagnosticHandler)

	RegisterDownloadRoutes(r, cfg.Database.DataDir)
	RegisterHealthRoutes(r, healthHandler)
	RegisterFrontendRoutes(r)

	return r
}

func AuthenticateAgentByToken(database *db.Database) func(token string) (string, error) {
	return func(token string) (string, error) {
		var agent db.Agent
		if err := database.DB.First(&agent, "agent_token = ?", token).Error; err != nil {
			return "", err
		}
		return agent.ID, nil
	}
}

func CurrentPolicyLookup(database *db.Database) func(agentID string) (*protocol.Message, bool) {
	return CurrentPolicyLookupWithTracker(database, defaultPolicyPushTracker)
}

func CurrentPolicyLookupWithTracker(database *db.Database, tracker *PolicyPushTracker) func(agentID string) (*protocol.Message, bool) {
	return func(agentID string) (*protocol.Message, bool) {
		command, ok := CurrentPolicyCommandLookupWithTracker(database, tracker)(agentID)
		if !ok || command == nil {
			return nil, false
		}
		return command.Message, true
	}
}

func CurrentPolicyCommandLookup(database *db.Database) func(agentID string) (*CurrentPolicyCommand, bool) {
	return CurrentPolicyCommandLookupWithTracker(database, defaultPolicyPushTracker)
}

func CurrentPolicyCommandLookupWithTracker(database *db.Database, tracker *PolicyPushTracker) func(agentID string) (*CurrentPolicyCommand, bool) {
	return func(agentID string) (*CurrentPolicyCommand, bool) {
		if database == nil || database.DB == nil {
			return nil, false
		}

		var policy db.BackupPolicy
		if err := database.DB.
			Where("agent_id = ? AND synced = ?", agentID, false).
			Order("updated_at DESC").
			First(&policy).Error; err != nil {
			return nil, false
		}

		var storage db.StorageConfig
		if err := database.DB.First(&storage, "id = ?", policy.StorageID).Error; err != nil {
			return nil, false
		}

		payload, err := policyPushPayload(database, policy, storage)
		if err != nil {
			return nil, false
		}

		msg, err := protocol.NewMessage(protocol.TypePolicyPush, payload)
		if err != nil {
			return nil, false
		}
		tracker.Track(msg.ID, policy.AgentID, policy.ID, policy.UpdatedAt)
		return &CurrentPolicyCommand{
			Message:         msg,
			AgentID:         policy.AgentID,
			PolicyID:        policy.ID,
			StorageID:       storage.ID,
			PolicyUpdatedAt: policy.UpdatedAt,
		}, true
	}
}

func NewPolicyAckProcessor(database *db.Database, completer ...PolicyCommandCompleter) func(agentID string, msg protocol.Message) error {
	return NewPolicyAckProcessorWithTracker(database, defaultPolicyPushTracker, completer...)
}

func NewPolicyAckProcessorWithTracker(database *db.Database, tracker *PolicyPushTracker, completer ...PolicyCommandCompleter) func(agentID string, msg protocol.Message) error {
	return func(agentID string, msg protocol.Message) error {
		ack, err := protocol.ParsePayload[protocol.PolicyAckPayload](&msg)
		if err != nil {
			return err
		}
		tracked, ok := tracker.Get(msg.ID, agentID)
		if !ok {
			tracked, ok = durableTrackedPolicyPush(database, agentID, msg.ID)
		}
		var completerErr error
		if len(completer) > 0 && completer[0] != nil {
			completerErr = completer[0].CompletePolicyAck(context.Background(), agentID, msg.ID, ack.Success, ack.Error)
		}
		if !ok {
			return completerErr
		}
		if !ack.Success {
			tracker.Delete(msg.ID)
			return completerErr
		}

		err = database.DB.Model(&db.BackupPolicy{}).
			Where("id = ? AND agent_id = ? AND synced = ? AND updated_at = ?", tracked.PolicyID, tracked.AgentID, false, tracked.UpdatedAt).
			Update("synced", true).Error
		if err == nil {
			tracker.Delete(msg.ID)
			return completerErr
		}
		return err
	}
}

func durableTrackedPolicyPush(database *db.Database, agentID string, messageID string) (trackedPolicyPush, bool) {
	if database == nil || database.DB == nil || agentID == "" || messageID == "" {
		return trackedPolicyPush{}, false
	}
	var command db.AgentCommand
	if err := database.DB.First(&command, "agent_id = ? AND message_id = ? AND type = ?", agentID, messageID, protocol.TypePolicyPush).Error; err != nil {
		return trackedPolicyPush{}, false
	}
	if command.Status != commands.CommandStatusPending && command.Status != commands.CommandStatusDispatched && command.Status != commands.CommandStatusRunning {
		return trackedPolicyPush{}, false
	}
	if command.PolicyID == "" || command.PolicyUpdatedAt == nil {
		return trackedPolicyPush{}, false
	}
	return trackedPolicyPush{
		AgentID:   command.AgentID,
		PolicyID:  command.PolicyID,
		UpdatedAt: *command.PolicyUpdatedAt,
	}, true
}

func unavailableAgentWebSocket(database *db.Database) gin.HandlerFunc {
	authAgent := AuthenticateAgentByToken(database)
	return func(c *gin.Context) {
		token := c.Query("token")
		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "missing token"})
			return
		}
		if _, err := authAgent(token); err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "invalid token"})
			return
		}

		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false, "error": "websocket handler not configured"})
	}
}

func policyPushPayload(database *db.Database, policy db.BackupPolicy, storage db.StorageConfig) (protocol.PolicyPushPayload, error) {
	resticPassword, err := db.Decrypt(policy.ResticPassword, database.MasterKey)
	if err != nil {
		return protocol.PolicyPushPayload{}, err
	}

	rcloneConfig, err := decryptPolicyRcloneConfig(database, storage.RcloneConfig)
	if err != nil {
		return protocol.PolicyPushPayload{}, err
	}
	repoPath := policyRepoPath(storage.RcloneType, rcloneConfig, policy.RepoPath)
	rcloneConfig = storageRcloneConfig(storage.RcloneType, rcloneConfig)
	rclonePassObscured := false
	supportsPlaintextPass, err := agentSupportsPlaintextRclonePass(database, policy.AgentID)
	if err != nil {
		return protocol.PolicyPushPayload{}, err
	}
	if !supportsPlaintextPass {
		rcloneConfig, err = rcloneobscure.PrepareConfigForLegacyAgent(rcloneConfig)
		if err != nil {
			return protocol.PolicyPushPayload{}, err
		}
		rclonePassObscured = true
	}

	var backupDirs []string
	if err := json.Unmarshal([]byte(policy.BackupDirs), &backupDirs); err != nil {
		return protocol.PolicyPushPayload{}, err
	}

	excludePatterns := []string{}
	if policy.ExcludePatterns != "" {
		if err := json.Unmarshal([]byte(policy.ExcludePatterns), &excludePatterns); err != nil {
			return protocol.PolicyPushPayload{}, err
		}
	}

	var retention protocol.RetentionPolicy
	if err := json.Unmarshal([]byte(policy.Retention), &retention); err != nil {
		return protocol.PolicyPushPayload{}, err
	}

	rcloneArgs, err := unmarshalPolicyRcloneArgs(policy.RcloneArgs)
	if err != nil {
		return protocol.PolicyPushPayload{}, err
	}

	return protocol.PolicyPushPayload{
		AgentID: policy.AgentID,
		Storage: protocol.StorageConfig{
			RcloneType:         storage.RcloneType,
			RcloneConfig:       rcloneConfig,
			RepoPath:           repoPath,
			RcloneArgs:         rcloneArgs,
			RclonePassObscured: rclonePassObscured,
		},
		ResticPassword:  resticPassword,
		PlainBackup:     strings.TrimSpace(resticPassword) == "",
		BackupDirs:      backupDirs,
		ExcludePatterns: excludePatterns,
		Schedule:        policy.Schedule,
		Retention:       retention,
	}, nil
}

func decryptPolicyRcloneConfig(database *db.Database, rawConfig string) (map[string]string, error) {
	plaintext, err := db.Decrypt(rawConfig, database.MasterKey)
	if err != nil {
		return nil, err
	}

	var anyValues map[string]any
	if err := json.Unmarshal([]byte(plaintext), &anyValues); err != nil {
		return nil, err
	}

	values := make(map[string]string, len(anyValues))
	for key, value := range anyValues {
		if stringValue, ok := value.(string); ok {
			values[key] = stringValue
			continue
		}
		return nil, fmt.Errorf("rclone config %q must be a string", key)
	}
	return values, nil
}

func policyRepoPath(rcloneType string, rcloneConfig map[string]string, repoPath string) string {
	if rcloneType != "s3" {
		return repoPath
	}
	bucket := storagecheck.S3BucketPathSegment(rcloneConfig["bucket"])
	if bucket == "" {
		return repoPath
	}
	return bucket + "/" + strings.TrimLeft(repoPath, "/")
}

func storageRcloneConfig(rcloneType string, rcloneConfig map[string]string) map[string]string {
	if rcloneType != "s3" {
		return rcloneConfig
	}
	values := make(map[string]string, len(rcloneConfig))
	for key, value := range rcloneConfig {
		if key == "bucket" {
			continue
		}
		values[key] = value
	}
	return values
}
