package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/internal/master/ws"
	"vaultfleet/pkg/protocol"
	"vaultfleet/pkg/rcloneobscure"
)

func TestRouterAssemblyAuthCheckUninitialized(t *testing.T) {
	setup := setupRouterAssembly(t)

	w := routerAssemblyRequest(setup.router, http.MethodGet, "/api/auth/check", nil)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := parseJSON(t, w)
	require.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	assert.Equal(t, false, data["initialized"])
}

func TestRouterAssemblyProtectedRoutesRequireInitBeforeAuth(t *testing.T) {
	setup := setupRouterAssembly(t)

	for _, path := range protectedRouterAssemblyRoutes() {
		t.Run(path, func(t *testing.T) {
			w := routerAssemblyRequest(setup.router, http.MethodGet, path, nil)

			require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
			body := parseJSON(t, w)
			assert.Equal(t, false, body["ok"])
			assert.Equal(t, "init_required", body["error"])
		})
	}
}

func TestRouterAssemblyProtectedRoutesRequireAuthOnceInitialized(t *testing.T) {
	setup := setupRouterAssembly(t)
	createRouterAssemblyUser(t, setup.database)

	for _, path := range protectedRouterAssemblyRoutes() {
		t.Run(path, func(t *testing.T) {
			w := routerAssemblyRequest(setup.router, http.MethodGet, path, nil)

			require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		})
	}
}

func protectedRouterAssemblyRoutes() []string {
	return []string{
		"/api/agents",
		"/api/notifications",
		"/api/system/export",
		"/api/commands/command-1",
		"/api/agents/agent-1/commands",
	}
}

func TestRouterAssemblySessionCookieAccessesProtectedRoute(t *testing.T) {
	setup := setupRouterAssembly(t)

	initResponse := postJSON(t, setup.router, "/api/auth/init", map[string]string{
		"username": "admin",
		"password": "secret123",
	})
	require.Equal(t, http.StatusOK, initResponse.Code, initResponse.Body.String())
	cookie := getSessionCookie(t, initResponse)

	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestRouterAssemblySnapshotRefreshQueuesDurableCommand(t *testing.T) {
	setup := setupRouterAssembly(t)

	initResponse := postJSON(t, setup.router, "/api/auth/init", map[string]string{
		"username": "admin",
		"password": "secret123",
	})
	require.Equal(t, http.StatusOK, initResponse.Code, initResponse.Body.String())
	cookie := getSessionCookie(t, initResponse)

	agent := db.Agent{Name: "Snapshot Router Agent", Status: "offline"}
	require.NoError(t, setup.database.DB.Create(&agent).Error)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/"+agent.ID+"/snapshots/refresh", nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, true, body["ok"])
	data := requireMap(t, body["data"])
	commandID, ok := data["command_id"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, commandID)
	messageID, ok := data["message_id"].(string)
	require.True(t, ok)
	assert.NotEmpty(t, messageID)

	var command db.AgentCommand
	require.NoError(t, setup.database.DB.First(&command, "id = ?", commandID).Error)
	assert.Equal(t, agent.ID, command.AgentID)
	assert.Equal(t, protocol.TypeSnapshotListReq, command.Type)
	assert.Equal(t, commands.CommandStatusPending, command.Status)
	assert.Equal(t, messageID, command.MessageID)
}

func TestRouterAssemblyFrontendFallback(t *testing.T) {
	setup := setupRouterAssembly(t)

	w := routerAssemblyRequest(setup.router, http.MethodGet, "/dashboard", nil)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, w.Body.String(), "VaultFleet")
}

func TestRouterAssemblyMissingAPIRouteDoesNotFallThroughToFrontend(t *testing.T) {
	setup := setupRouterAssembly(t)

	w := routerAssemblyRequest(setup.router, http.MethodGet, "/api/not-a-route", nil)

	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	assert.NotContains(t, w.Header().Get("Content-Type"), "text/html")
	assert.NotContains(t, w.Body.String(), "VaultFleet")
}

func TestRouterAssemblyPublicAgentEnrollIsNotBlockedByAuthOrInit(t *testing.T) {
	setup := setupRouterAssembly(t)

	w := routerAssemblyRequest(setup.router, http.MethodPost, "/api/agent/enroll", bytes.NewReader([]byte(`{}`)))

	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.NotEqual(t, http.StatusUnauthorized, w.Code)
	assert.NotEqual(t, http.StatusConflict, w.Code)
}

func TestRouterAssemblyHealthRoutesBypassFrontendAndAuth(t *testing.T) {
	setup := setupRouterAssembly(t)

	for _, testCase := range []struct {
		path       string
		statusCode int
		contains   string
	}{
		{path: "/health", statusCode: http.StatusOK, contains: `"status":"healthy"`},
		{path: "/ready", statusCode: http.StatusOK, contains: `"status":"ready"`},
		{path: "/metrics", statusCode: http.StatusOK, contains: "vaultfleet_agents_total"},
	} {
		t.Run(testCase.path, func(t *testing.T) {
			w := routerAssemblyRequest(setup.router, http.MethodGet, testCase.path, nil)

			require.Equal(t, testCase.statusCode, w.Code, w.Body.String())
			assert.Contains(t, w.Body.String(), testCase.contains)
			assert.NotContains(t, w.Header().Get("Content-Type"), "text/html")
			assert.NotContains(t, w.Body.String(), "VaultFleet")
		})
	}
}

func TestRouterAssemblyAgentWebSocketUsesInjectedHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := newRouterAssemblyDatabase(t)
	router := NewRouter(RouterConfig{
		Database: database,
		Hub:      ws.NewHub(),
		EventBus: events.NewBus(),
		AgentWebSocket: func(c *gin.Context) {
			c.JSON(http.StatusTeapot, gin.H{"ok": true, "source": "injected"})
		},
	})

	w := routerAssemblyRequest(router, http.MethodGet, "/ws/agent", nil)

	require.Equal(t, http.StatusTeapot, w.Code, w.Body.String())
	body := parseJSON(t, w)
	assert.Equal(t, "injected", body["source"])
}

func TestNewRouterPanicsWithClearMessageWhenDatabaseMissing(t *testing.T) {
	require.PanicsWithValue(t, "router database is required", func() {
		NewRouter(RouterConfig{})
	})
}

func TestCurrentPolicyLookupNoPolicyReturnsFalse(t *testing.T) {
	database := newRouterAssemblyDatabase(t)

	msg, ok := CurrentPolicyLookup(database)("agent-1")

	assert.False(t, ok)
	assert.Nil(t, msg)
}

func TestCurrentPolicyLookupSyncedPolicyReturnsFalse(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, true)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	assert.False(t, ok)
	assert.Nil(t, msg)
}

func TestCurrentPolicyLookupUnsyncedPolicyReturnsPolicyPushWithDecryptedCredentials(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	require.True(t, ok)
	require.NotNil(t, msg)
	assert.Equal(t, protocol.TypePolicyPush, msg.Type)

	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](msg)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)
	assert.Equal(t, "restic-password", payload.ResticPassword)
	assert.Equal(t, "s3", payload.Storage.RcloneType)
	assert.Equal(t, "vaultfleet/"+agent.ID, payload.Storage.RepoPath)
	assert.Equal(t, map[string]string{
		"provider":          "Cloudflare",
		"access_key_id":     "AKID123",
		"secret_access_key": "SECRET456",
	}, payload.Storage.RcloneConfig)
	assert.Equal(t, []string{"/etc"}, payload.BackupDirs)
	assert.Equal(t, protocol.RetentionPolicy{KeepLast: 3}, payload.Retention)
}

func TestCurrentPolicyLookupObscuresSFTPPasswordForAgent(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "v.ps")
	storage := db.StorageConfig{
		Name:       "hz_box",
		RcloneType: "sftp",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"host": "u575095.your-storagebox.de",
			"user": "u575095",
			"pass": "clear-sftp-password",
			"port": "22",
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	require.True(t, ok)
	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](msg)
	require.NoError(t, err)
	assert.NotEqual(t, "clear-sftp-password", payload.Storage.RcloneConfig["pass"])
	revealed, err := rcloneobscure.RevealPass(payload.Storage.RcloneConfig["pass"])
	require.NoError(t, err)
	assert.Equal(t, "clear-sftp-password", revealed)
}

func TestCurrentPolicyLookupMovesS3BucketIntoRepoPath(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storage := db.StorageConfig{
		Name:       "MinIO",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Other",
			"endpoint":          "https://minio.example.test",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
			"bucket":            "test",
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	require.True(t, ok)
	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](msg)
	require.NoError(t, err)
	assert.Equal(t, "test/vaultfleet/"+agent.ID, payload.Storage.RepoPath)
	assert.NotContains(t, payload.Storage.RcloneConfig, "bucket")
	assert.Equal(t, "https://minio.example.test", payload.Storage.RcloneConfig["endpoint"])
}

func TestCurrentPolicyLookupNormalizesS3BucketLikeStorageCheck(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storage := db.StorageConfig{
		Name:       "MinIO",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Other",
			"endpoint":          "https://minio.example.test",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
			"bucket":            " /test/ ",
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	require.True(t, ok)
	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](msg)
	require.NoError(t, err)
	assert.Equal(t, "test/vaultfleet/"+agent.ID, payload.Storage.RepoPath)
	assert.NotContains(t, payload.Storage.RcloneConfig, "bucket")
}

func TestCurrentPolicyLookupRejectsNonStringRcloneConfigValues(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storage := db.StorageConfig{
		Name:       "Cloudflare R2",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":   "Cloudflare",
			"chunk_size": 123,
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	assert.False(t, ok)
	assert.Nil(t, msg)
}

func TestCurrentPolicyLookupRejectsNullRcloneConfigValues(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storage := db.StorageConfig{
		Name:       "Cloudflare R2",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider": "Cloudflare",
			"region":   nil,
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	msg, ok := CurrentPolicyLookup(database)(agent.ID)

	assert.False(t, ok)
	assert.Nil(t, msg)
}

func TestPolicyAckProcessorSuccessfulAckMarksNewestUnsyncedPolicySynced(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	older := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	newer := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	require.NoError(t, database.DB.Model(&older).Update("updated_at", time.Now().Add(-time.Hour)).Error)
	require.NoError(t, database.DB.Model(&newer).Update("updated_at", time.Now()).Error)

	tracker := NewPolicyPushTracker()
	pushed, ok := CurrentPolicyLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	msg := policyAckMessageWithID(t, pushed.ID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker)(agent.ID, *msg))

	var storedOlder db.BackupPolicy
	require.NoError(t, database.DB.First(&storedOlder, "id = ?", older.ID).Error)
	assert.False(t, storedOlder.Synced)
	var storedNewer db.BackupPolicy
	require.NoError(t, database.DB.First(&storedNewer, "id = ?", newer.ID).Error)
	assert.True(t, storedNewer.Synced)
}

func TestPolicyChangedPusherSendsCurrentPolicyToOnlineAgent(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()

	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, CurrentPolicyLookupWithTracker(database, tracker))
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	pusher.Handle(events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	})

	require.Len(t, hub.sent, 1)
	sent := hub.sent[0].message
	assert.Equal(t, protocol.TypePolicyPush, sent.Type)
	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](&sent)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)
	tracked, ok := tracker.Get(sent.ID, agent.ID)
	require.True(t, ok)
	assert.Equal(t, policy.ID, tracked.PolicyID)

	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND type = ?", agent.ID, protocol.TypePolicyPush).Error)
	assert.Equal(t, commands.CommandStatusDispatched, command.Status)
	assert.Equal(t, policy.ID, command.PolicyID)
	assert.Equal(t, storage.ID, command.StorageID)
	assert.Equal(t, sent.ID, command.MessageID)
}

func TestPolicyChangedPusherDoesNotDuplicateActivePolicyPushCommand(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()

	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, CurrentPolicyLookupWithTracker(database, tracker))
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	event := events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	}

	pusher.Handle(event)
	pusher.Handle(event)

	var commandCount int64
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("agent_id = ? AND type = ? AND policy_id = ? AND storage_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID, storage.ID).
		Count(&commandCount).Error)
	assert.Equal(t, int64(1), commandCount)
	assert.Len(t, hub.sent, 1)
}

func TestPolicyChangedPusherIgnoresExpiredActivePolicyPushCommand(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	now := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	commandService := commands.NewService(database, hub)
	commandService.Now = func() time.Time { return now.Add(-10 * time.Minute) }

	current, ok := CurrentPolicyCommandLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	require.NotNil(t, current)
	_, err := commandService.CreateCommand(context.Background(), commands.CreateCommandInput{
		AgentID:         agent.ID,
		Type:            protocol.TypePolicyPush,
		Message:         *current.Message,
		PolicyID:        current.PolicyID,
		PolicyUpdatedAt: &current.PolicyUpdatedAt,
		StorageID:       current.StorageID,
	})
	require.NoError(t, err)
	commandService.Now = func() time.Time { return now }

	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService

	require.True(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))

	var commandCount int64
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("agent_id = ? AND type = ? AND policy_id = ? AND storage_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID, storage.ID).
		Count(&commandCount).Error)
	assert.Equal(t, int64(2), commandCount)

	var latest db.AgentCommand
	require.NoError(t, database.DB.
		Where("agent_id = ? AND type = ? AND policy_id = ? AND storage_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID, storage.ID).
		Order("deadline_at DESC").
		First(&latest).Error)
	require.NotNil(t, latest.DeadlineAt)
	assert.True(t, latest.DeadlineAt.After(now))
	assert.Equal(t, commands.CommandStatusPending, latest.Status)
}

func TestPolicyChangedPusherDoesNotDuplicateConcurrentActivePolicyPushCommands(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()

	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, CurrentPolicyLookupWithTracker(database, tracker))
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	event := events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	}

	var start sync.WaitGroup
	start.Add(1)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			start.Wait()
			pusher.Handle(event)
		}()
	}
	start.Done()
	workers.Wait()

	var commandCount int64
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("agent_id = ? AND type = ? AND policy_id = ? AND storage_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID, storage.ID).
		Count(&commandCount).Error)
	assert.Equal(t, int64(1), commandCount)
}

func TestPolicyChangedPusherCreatesNewCommandForUpdatedPolicyVersion(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, oldStorage := createRouterAssemblyPolicyFixtures(t, database)
	newStorage := db.StorageConfig{
		Name:       "New Storage",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Cloudflare",
			"access_key_id":     "NEWID123",
			"secret_access_key": "NEWSECRET456",
		}),
	}
	require.NoError(t, database.DB.Create(&newStorage).Error)
	policy := createStorageTestPolicy(t, database, agent.ID, oldStorage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	event := events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	}

	pusher.Handle(event)
	var first db.AgentCommand
	require.NoError(t, database.DB.First(&first, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).Error)
	require.NotNil(t, first.PolicyUpdatedAt)

	newVersion := first.PolicyUpdatedAt.Add(time.Second)
	require.NoError(t, database.DB.Model(&policy).Updates(map[string]any{
		"updated_at": newVersion,
		"schedule":   "0 4 * * *",
		"storage_id": newStorage.ID,
	}).Error)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", first.ID).
		Update("created_at", newVersion.Add(time.Second)).Error)

	pusher.Handle(event)

	var commandCount int64
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).
		Count(&commandCount).Error)
	assert.Equal(t, int64(2), commandCount)

	var retiredFirst db.AgentCommand
	require.NoError(t, database.DB.First(&retiredFirst, "id = ?", first.ID).Error)
	assert.Equal(t, commands.CommandStatusFailed, retiredFirst.Status)
	assert.Contains(t, retiredFirst.ErrorMessage, "superseded")

	var second db.AgentCommand
	require.NoError(t, database.DB.Where("agent_id = ? AND type = ? AND policy_id = ? AND policy_updated_at = ?", agent.ID, protocol.TypePolicyPush, policy.ID, newVersion).
		First(&second).Error)
	assert.Equal(t, newStorage.ID, second.StorageID)
	require.NotNil(t, second.PolicyUpdatedAt)
	assert.True(t, second.PolicyUpdatedAt.Equal(newVersion))
}

func TestPolicyChangedPusherRetiresActivePolicyPushWhenPolicyDeleted(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	commandService := commands.NewService(database, hub)
	commandService.Now = func() time.Time { return now }
	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService

	require.True(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))
	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).Error)
	assert.Equal(t, commands.CommandStatusPending, command.Status)

	require.NoError(t, database.DB.Delete(&policy).Error)
	now = now.Add(time.Minute)

	require.False(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))

	var retired db.AgentCommand
	require.NoError(t, database.DB.First(&retired, "id = ?", command.ID).Error)
	assert.Equal(t, commands.CommandStatusFailed, retired.Status)
	assert.Contains(t, retired.ErrorMessage, "no current policy")
	require.NotNil(t, retired.CompletedAt)
	assert.True(t, retired.CompletedAt.Equal(now))
}

func TestPolicyChangedPusherRetiresPolicyPushForDifferentCurrentPolicy(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policyA := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	now := time.Date(2026, 5, 20, 12, 30, 0, 0, time.UTC)
	commandService := commands.NewService(database, hub)
	commandService.Now = func() time.Time { return now }
	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService

	require.True(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))
	var commandA db.AgentCommand
	require.NoError(t, database.DB.First(&commandA, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policyA.ID).Error)

	policyB := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	require.NotNil(t, commandA.PolicyUpdatedAt)
	policyBUpdatedAt := commandA.PolicyUpdatedAt.Add(time.Minute)
	require.NoError(t, database.DB.Model(&policyB).Update("updated_at", policyBUpdatedAt).Error)
	now = now.Add(time.Minute)

	require.True(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))

	var retiredA db.AgentCommand
	require.NoError(t, database.DB.First(&retiredA, "id = ?", commandA.ID).Error)
	assert.Equal(t, commands.CommandStatusFailed, retiredA.Status)
	assert.Contains(t, retiredA.ErrorMessage, "stale policy push command retired")
	require.NotNil(t, retiredA.CompletedAt)

	var commandB db.AgentCommand
	require.NoError(t, database.DB.First(&commandB, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policyB.ID).Error)
	assert.Equal(t, commands.CommandStatusPending, commandB.Status)
	assert.Equal(t, policyB.ID, commandB.PolicyID)
	require.NotNil(t, commandB.PolicyUpdatedAt)
	assert.True(t, commandB.PolicyUpdatedAt.Equal(policyBUpdatedAt))

	var active []db.AgentCommand
	require.NoError(t, database.DB.
		Where("agent_id = ? AND type = ? AND status IN ?", agent.ID, protocol.TypePolicyPush, []string{commands.CommandStatusPending, commands.CommandStatusDispatched, commands.CommandStatusRunning}).
		Find(&active).Error)
	require.Len(t, active, 1)
	assert.Equal(t, policyB.ID, active[0].PolicyID)
}

func TestPolicyChangedPusherCommandRefsMatchPolicyPayloadAndTrackerMessage(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storageA := db.StorageConfig{
		Name:       "Storage A",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Cloudflare",
			"access_key_id":     "AKID-A",
			"secret_access_key": "SECRET-A",
			"bucket":            "bucket-a",
		}),
	}
	require.NoError(t, database.DB.Create(&storageA).Error)
	storageB := db.StorageConfig{
		Name:       "Storage B",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Other",
			"access_key_id":     "AKID-B",
			"secret_access_key": "SECRET-B",
			"bucket":            "bucket-b",
		}),
	}
	require.NoError(t, database.DB.Create(&storageB).Error)
	policyA := createStorageTestPolicy(t, database, agent.ID, storageA.ID, false)
	var policyB db.BackupPolicy
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	previousDefaultTracker := defaultPolicyPushTracker
	defaultPolicyPushTracker = tracker
	defer func() {
		defaultPolicyPushTracker = previousDefaultTracker
	}()
	lookup := func(agentID string) (*protocol.Message, bool) {
		msg, ok := CurrentPolicyLookupWithTracker(database, tracker)(agentID)
		policyB = createStorageTestPolicy(t, database, agent.ID, storageB.ID, false)
		require.NoError(t, database.DB.Model(&policyB).Update("updated_at", time.Now().Add(time.Hour)).Error)
		return msg, ok
	}

	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, lookup)
	pusher.Commands = commandService
	pusher.Handle(events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	})

	require.Len(t, hub.sent, 1)
	sent := hub.sent[0].message
	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](&sent)
	require.NoError(t, err)

	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND message_id = ?", agent.ID, sent.ID).Error)
	switch command.StorageID {
	case storageA.ID:
		assert.Equal(t, policyA.ID, command.PolicyID)
		assert.Equal(t, "bucket-a/vaultfleet/"+agent.ID, payload.Storage.RepoPath)
		assert.Equal(t, "AKID-A", payload.Storage.RcloneConfig["access_key_id"])
	case storageB.ID:
		require.NotEmpty(t, policyB.ID)
		assert.Equal(t, policyB.ID, command.PolicyID)
		assert.Equal(t, "bucket-b/vaultfleet/"+agent.ID, payload.Storage.RepoPath)
		assert.Equal(t, "AKID-B", payload.Storage.RcloneConfig["access_key_id"])
	default:
		t.Fatalf("command storage %q did not match a fixture", command.StorageID)
	}
	tracked, ok := tracker.Get(sent.ID, agent.ID)
	require.True(t, ok)
	assert.Equal(t, command.PolicyID, tracked.PolicyID)
	assert.Equal(t, sent.ID, command.MessageID)
}

func TestPolicyAckProcessorCompletesPolicyPushCommand(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, CurrentPolicyLookupWithTracker(database, tracker))
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	pusher.Handle(events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	})
	require.Len(t, hub.sent, 1)
	messageID := hub.sent[0].message.ID
	ack := policyAckMessageWithID(t, messageID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker, commandService)(agent.ID, *ack))

	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND message_id = ?", agent.ID, messageID).Error)
	assert.Equal(t, commands.CommandStatusSucceeded, command.Status)
	assert.NotNil(t, command.CompletedAt)
	assert.Empty(t, command.ErrorMessage)

	var policy db.BackupPolicy
	require.NoError(t, database.DB.First(&policy, "agent_id = ?", agent.ID).Error)
	assert.True(t, policy.Synced)
}

func TestPolicyAckProcessorCompleterErrorDoesNotBlockPolicySync(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	tracker := NewPolicyPushTracker()
	pushed, ok := CurrentPolicyLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	ack := policyAckMessageWithID(t, pushed.ID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})
	completerErr := errors.New("command completion unavailable")

	err := NewPolicyAckProcessorWithTracker(database, tracker, failingPolicyCommandCompleter{err: completerErr})(agent.ID, *ack)

	require.ErrorIs(t, err, completerErr)
	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.True(t, stored.Synced)
}

func TestPolicyAckProcessorUsesDurableCommandWhenTrackerIsEmpty(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	trackerBeforeRestart := NewPolicyPushTracker()
	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, trackerBeforeRestart)
	pusher.Commands = commandService
	require.True(t, pusher.EnsureDurableCommand(context.Background(), agent.ID))

	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND type = ? AND policy_id = ?", agent.ID, protocol.TypePolicyPush, policy.ID).Error)
	emptyTrackerAfterRestart := NewPolicyPushTracker()
	ack := policyAckMessageWithID(t, command.MessageID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, emptyTrackerAfterRestart, commandService)(agent.ID, *ack))

	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.True(t, stored.Synced)
}

func TestPolicyAckProcessorSuccessfulOldAckDoesNotMarkNewerPolicySynced(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policyA := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	tracker := NewPolicyPushTracker()
	msgA, ok := CurrentPolicyLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	require.NotNil(t, msgA)

	policyB := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	require.NoError(t, database.DB.Model(&policyB).Update("updated_at", time.Now()).Error)

	ack := policyAckMessageWithID(t, msgA.ID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker)(agent.ID, *ack))

	var storedA db.BackupPolicy
	require.NoError(t, database.DB.First(&storedA, "id = ?", policyA.ID).Error)
	assert.True(t, storedA.Synced)
	var storedB db.BackupPolicy
	require.NoError(t, database.DB.First(&storedB, "id = ?", policyB.ID).Error)
	assert.False(t, storedB.Synced)
}

func TestPolicyAckProcessorUnknownMessageIDMarksNoPolicySynced(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	tracker := NewPolicyPushTracker()

	ack := policyAckMessageWithID(t, "unknown-policy-push-message-id", protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker)(agent.ID, *ack))

	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.False(t, stored.Synced)
}

func TestPolicyAckProcessorFailedAckLeavesUnsyncedPolicyUnsynced(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)

	tracker := NewPolicyPushTracker()
	pushed, ok := CurrentPolicyLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	msg := policyAckMessageWithID(t, pushed.ID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: false, Error: "rejected"})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker)(agent.ID, *msg))

	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.False(t, stored.Synced)
}

func TestPolicyAckProcessorFailedAckConsumesTrackedPush(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	hub := &fakeCommandHub{online: map[string]bool{agent.ID: true}}
	tracker := NewPolicyPushTracker()
	commandService := commands.NewService(database, hub)
	pusher := NewPolicyChangedPusher(database, hub, nil)
	pusher.CommandLookup = CurrentPolicyCommandLookupWithTracker(database, tracker)
	pusher.Commands = commandService
	pusher.Handle(events.Event{
		Type: events.PolicyChanged,
		Payload: map[string]interface{}{
			"agent_id": agent.ID,
			"action":   "updated",
		},
	})
	require.Len(t, hub.sent, 1)
	messageID := hub.sent[0].message.ID

	failed := policyAckMessageWithID(t, messageID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: false, Error: "temporary"})
	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker, commandService)(agent.ID, *failed))

	success := policyAckMessageWithID(t, messageID, protocol.PolicyAckPayload{AgentID: agent.ID, Success: true})
	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker, commandService)(agent.ID, *success))

	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.False(t, stored.Synced)
	var command db.AgentCommand
	require.NoError(t, database.DB.First(&command, "agent_id = ? AND message_id = ?", agent.ID, messageID).Error)
	assert.Equal(t, commands.CommandStatusFailed, command.Status)
}

func TestPolicyAckProcessorUsesAuthenticatedAgentIDOverPayloadAgentID(t *testing.T) {
	database := newRouterAssemblyDatabase(t)
	agent, storage := createRouterAssemblyPolicyFixtures(t, database)
	otherAgent := createStorageTestAgent(t, database, "Osaka-1")
	policy := createStorageTestPolicy(t, database, agent.ID, storage.ID, false)
	otherPolicy := createStorageTestPolicy(t, database, otherAgent.ID, storage.ID, false)

	tracker := NewPolicyPushTracker()
	pushed, ok := CurrentPolicyLookupWithTracker(database, tracker)(agent.ID)
	require.True(t, ok)
	msg := policyAckMessageWithID(t, pushed.ID, protocol.PolicyAckPayload{AgentID: otherAgent.ID, Success: true})

	require.NoError(t, NewPolicyAckProcessorWithTracker(database, tracker)(agent.ID, *msg))

	var stored db.BackupPolicy
	require.NoError(t, database.DB.First(&stored, "id = ?", policy.ID).Error)
	assert.True(t, stored.Synced)
	var storedOther db.BackupPolicy
	require.NoError(t, database.DB.First(&storedOther, "id = ?", otherPolicy.ID).Error)
	assert.False(t, storedOther.Synced)
}

type routerAssemblySetup struct {
	database *db.Database
	router   *gin.Engine
}

type failingPolicyCommandCompleter struct {
	err error
}

func (c failingPolicyCommandCompleter) CompletePolicyAck(context.Context, string, string, bool, string) error {
	return c.err
}

func setupRouterAssembly(t *testing.T) routerAssemblySetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)

	router := NewRouter(RouterConfig{
		Database: database,
		Hub:      ws.NewHub(),
		EventBus: events.NewBus(),
	})

	return routerAssemblySetup{
		database: database,
		router:   router,
	}
}

func routerAssemblyRequest(router http.Handler, method string, path string, body *bytes.Reader) *httptest.ResponseRecorder {
	if body == nil {
		body = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, body)
	if body.Len() > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func createRouterAssemblyUser(t *testing.T, database *db.Database) {
	t.Helper()

	require.NoError(t, database.DB.Create(&db.User{
		Username:     "admin",
		PasswordHash: "hashed-password",
	}).Error)
}

func newRouterAssemblyDatabase(t *testing.T) *db.Database {
	t.Helper()

	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	return database
}

func createRouterAssemblyPolicyFixtures(t *testing.T, database *db.Database) (db.Agent, db.StorageConfig) {
	t.Helper()

	agent := createStorageTestAgent(t, database, "Tokyo-1")
	storage := db.StorageConfig{
		Name:       "Cloudflare R2",
		RcloneType: "s3",
		RcloneConfig: mustEncryptMap(t, database, map[string]any{
			"provider":          "Cloudflare",
			"access_key_id":     "AKID123",
			"secret_access_key": "SECRET456",
		}),
	}
	require.NoError(t, database.DB.Create(&storage).Error)
	return agent, storage
}

func policyAckMessage(t *testing.T, payload protocol.PolicyAckPayload) *protocol.Message {
	t.Helper()

	return policyAckMessageWithID(t, "policy-push-message-id", payload)
}

func policyAckMessageWithID(t *testing.T, messageID string, payload protocol.PolicyAckPayload) *protocol.Message {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	return &protocol.Message{
		Type:    protocol.TypePolicyAck,
		ID:      messageID,
		Payload: raw,
	}
}
