package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentenroll "vaultfleet/internal/agent/enroll"
	masterapi "vaultfleet/internal/master/api"
	"vaultfleet/internal/master/commands"
	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

type handlerTestSetup struct {
	hub    *Hub
	bus    *events.Bus
	router *gin.Engine
}

func setupHandlerTest(t *testing.T, auth AgentAuthFunc, lookup PolicyLookupFunc) handlerTestSetup {
	t.Helper()

	gin.SetMode(gin.TestMode)

	hub := NewHub()
	bus := events.NewBus()
	handler := NewHandler(hub, bus, auth, lookup, nil)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)

	return handlerTestSetup{
		hub:    hub,
		bus:    bus,
		router: router,
	}
}

func validTestAuth(token string) (string, error) {
	if token != "valid-token" {
		return "", errors.New("invalid token")
	}
	return "agent-1", nil
}

func noPolicy(string) (*protocol.Message, bool) {
	return nil, false
}

func websocketURL(serverURL, path string, query url.Values) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		panic(err)
	}
	u.Scheme = "ws"
	u.Path = path
	u.RawQuery = query.Encode()
	return u.String()
}

func TestHandler_MissingTokenRejected(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["ok"])
	assert.NotEmpty(t, body["error"])
}

func TestHandler_InvalidTokenRejected(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)

	req := httptest.NewRequest(http.MethodGet, "/ws?token=bad-token", nil)
	w := httptest.NewRecorder()
	setup.router.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, false, body["ok"])
	assert.NotEmpty(t, body["error"])
}

func TestHandler_ValidTokenAcceptedAndHubOnline(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		return setup.hub.IsOnline("agent-1")
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")))
	require.Eventually(t, func() bool {
		return !setup.hub.IsOnline("agent-1")
	}, time.Second, 10*time.Millisecond)
}

func TestHandlerDispatchesPendingCommandsOnConnect(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	dispatched := make(chan string, 1)
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.PendingCommandDispatcher = func(agentID string) error {
		dispatched <- agentID
		return nil
	}
	setup.router.GET("/ws-command-dispatch", handler.HandleWebSocket)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(
		websocketURL(server.URL, "/ws-command-dispatch", url.Values{"token": []string{"valid-token"}}),
		nil,
	)
	require.NoError(t, err)
	defer conn.Close()

	select {
	case agentID := <-dispatched:
		assert.Equal(t, "agent-1", agentID)
	case <-time.After(time.Second):
		t.Fatal("pending command dispatcher was not called")
	}
}

func TestHandler_HeartbeatDispatchUpdatesLastSeen(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	fixedNow := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	setup.router = gin.New()
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.now = func() time.Time { return fixedNow }
	setup.router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteJSON(protocol.Message{Type: protocol.TypeHeartbeat}))

	require.Eventually(t, func() bool {
		status := setup.hub.GetAllAgents()["agent-1"]
		return status != nil && status.LastSeenAt.Equal(fixedNow)
	}, time.Second, 10*time.Millisecond)
}

func TestHandlerConnectionUpdatesAgentDatabaseState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Tokyo-1", AgentToken: "ak_valid", Status: "offline"}
	require.NoError(t, database.DB.Create(&agent).Error)

	hub := NewHub()
	bus := events.NewBus()
	handler := NewHandler(
		hub,
		bus,
		masterapi.AuthenticateAgentByToken(database),
		noPolicy,
		nil,
	)
	handler.AgentStateUpdater = masterapi.NewAgentStateUpdater(database)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"ak_valid"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		var stored db.Agent
		require.NoError(t, database.DB.First(&stored, "id = ?", agent.ID).Error)
		return stored.Status == "online" && stored.LastSeenAt != nil
	}, time.Second, 10*time.Millisecond)
}

func TestHandler_HeartbeatDispatchMarksAgentOnline(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	setup.hub.Add("agent-1", &SafeConn{})
	setup.hub.MarkOffline("agent-1")
	require.False(t, setup.hub.IsOnline("agent-1"))

	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.dispatch("agent-1", protocol.Message{Type: protocol.TypeHeartbeat})

	assert.True(t, setup.hub.IsOnline("agent-1"))
}

func TestHandler_HeartbeatRefreshesReadDeadline(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	setup.router = gin.New()
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.pongWait = 120 * time.Millisecond
	setup.router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		return setup.hub.IsOnline("agent-1")
	}, time.Second, 10*time.Millisecond)

	time.Sleep(70 * time.Millisecond)
	require.NoError(t, conn.WriteJSON(protocol.Message{Type: protocol.TypeHeartbeat}))
	time.Sleep(80 * time.Millisecond)

	assert.True(t, setup.hub.IsOnline("agent-1"))
}

func TestHandler_PolicyAckDispatchPublishesPolicyChanged(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	eventsCh := make(chan events.Event, 1)
	setup.bus.Subscribe(events.PolicyChanged, func(event events.Event) {
		eventsCh <- event
	})

	setup.hub.Add("agent-1", &SafeConn{})
	setupHandler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	setupHandler.dispatch("agent-1", protocol.Message{Type: protocol.TypePolicyAck})

	select {
	case event := <-eventsCh:
		assert.Equal(t, events.PolicyChanged, event.Type)
		payload, ok := event.Payload.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "agent-1", payload["agent_id"])
		assert.Equal(t, "ack", payload["action"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy changed event")
	}
}

func TestHandler_PolicyAckProcessorRunsBeforeEventPublish(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	processed := make(chan string, 1)
	eventsCh := make(chan events.Event, 1)
	setup.bus.Subscribe(events.PolicyChanged, func(event events.Event) {
		eventsCh <- event
	})
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.PolicyAckProcessor = func(agentID string, msg protocol.Message) error {
		assert.Equal(t, protocol.TypePolicyAck, msg.Type)
		processed <- agentID
		return nil
	}

	handler.dispatch("agent-1", protocol.Message{
		Type:    protocol.TypePolicyAck,
		Payload: json.RawMessage(`{"agent_id":"agent-1","success":true}`),
	})

	select {
	case agentID := <-processed:
		assert.Equal(t, "agent-1", agentID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy ack processor")
	}

	select {
	case event := <-eventsCh:
		assert.Equal(t, events.PolicyChanged, event.Type)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for policy changed event")
	}
}

func TestHandler_TaskResultDispatchPublishesRawPayload(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	eventsCh := make(chan events.Event, 1)
	setup.bus.Subscribe(events.TaskResult, func(event events.Event) {
		eventsCh <- event
	})
	rawPayload := json.RawMessage(`{"task_type":"backup","status":"success"}`)

	setupHandler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	setupHandler.dispatch("agent-1", protocol.Message{
		Type:    protocol.TypeTaskResult,
		Payload: rawPayload,
	})

	select {
	case event := <-eventsCh:
		assert.Equal(t, events.TaskResult, event.Type)
		payload, ok := event.Payload.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "agent-1", payload["agent_id"])
		assert.Equal(t, rawPayload, payload["payload"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result event")
	}
}

func TestHandler_TaskResultProcessorFromConstructorRecordsHistoryAndSnapshots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Tokyo-1", Status: "online"}
	require.NoError(t, database.DB.Create(&agent).Error)
	hub := NewHub()
	bus := events.NewBus()
	handler := NewHandler(hub, bus, validTestAuth, noPolicy, masterapi.NewTaskResultProcessor(database))
	finishedAt := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	msg, err := protocol.NewMessage(protocol.TypeTaskResult, protocol.TaskResultPayload{
		AgentID:    agent.ID,
		TaskType:   "backup",
		Status:     "success",
		SnapshotID: "snap-ws",
		StartedAt:  finishedAt.Add(-time.Second),
		FinishedAt: finishedAt,
		Snapshots: []protocol.SnapshotInfo{
			{ID: "snap-ws", Time: finishedAt, Paths: []string{"/srv"}, Size: 2048},
		},
	})
	require.NoError(t, err)

	handler.dispatch(agent.ID, *msg)

	var history db.TaskHistory
	require.NoError(t, database.DB.First(&history, "agent_id = ? AND snapshot_id = ?", agent.ID, "snap-ws").Error)
	assert.Equal(t, "backup", history.Type)
	assert.Equal(t, "success", history.Status)

	var snapshot db.Snapshot
	require.NoError(t, database.DB.First(&snapshot, "agent_id = ? AND snapshot_id = ?", agent.ID, "snap-ws").Error)
	assert.True(t, snapshot.Timestamp.Equal(finishedAt))
	assert.JSONEq(t, `["/srv"]`, snapshot.Paths)
	assert.Equal(t, int64(2048), snapshot.Size)
}

func TestHandler_TaskResultProcessorErrorDoesNotPublishTaskResultEvent(t *testing.T) {
	hub := NewHub()
	bus := events.NewBus()
	eventsCh := make(chan events.Event, 1)
	bus.Subscribe(events.TaskResult, func(event events.Event) {
		eventsCh <- event
	})
	handler := NewHandler(hub, bus, validTestAuth, noPolicy, func(string, protocol.Message) error {
		return errors.New("database unavailable")
	})

	handler.dispatch("agent-1", protocol.Message{
		Type:    protocol.TypeTaskResult,
		Payload: json.RawMessage(`{"task_type":"backup","status":"success"}`),
	})

	select {
	case event := <-eventsCh:
		t.Fatalf("task result event published after persistence failure: %#v", event)
	default:
	}
}

func TestHandler_DirBrowseRespDispatchesToWaiter(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	clientConn := addTestWebSocketAgent(t, setup.hub, "agent-1")
	respCh, err := setup.hub.SendAndWait("agent-1", protocol.Message{
		Type:    protocol.TypeDirBrowseReq,
		ID:      "browse-1",
		Payload: json.RawMessage(`{"path":"/etc","depth":2}`),
	}, time.Second)
	require.NoError(t, err)
	var sent protocol.Message
	require.NoError(t, clientConn.ReadJSON(&sent))
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	response := protocol.Message{
		Type:    protocol.TypeDirBrowseResp,
		ID:      "browse-1",
		Payload: json.RawMessage(`{"path":"/etc","entries":[]}`),
	}

	handler.dispatch("agent-1", response)

	select {
	case got := <-respCh:
		assert.Equal(t, response, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for browse response")
	}
}

func TestHandler_SnapshotListRespDispatchesToWaiter(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	clientConn := addTestWebSocketAgent(t, setup.hub, "agent-1")
	respCh, err := setup.hub.SendAndWait("agent-1", protocol.Message{
		Type:    protocol.TypeSnapshotListReq,
		ID:      "snapshots-1",
		Payload: json.RawMessage(`{"agent_id":"agent-1"}`),
	}, time.Second)
	require.NoError(t, err)
	var sent protocol.Message
	require.NoError(t, clientConn.ReadJSON(&sent))
	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	response := protocol.Message{
		Type:    protocol.TypeSnapshotListResp,
		ID:      "snapshots-1",
		Payload: json.RawMessage(`{"agent_id":"agent-1","snapshots":[]}`),
	}

	handler.dispatch("agent-1", response)

	select {
	case got := <-respCh:
		assert.Equal(t, response, got)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for snapshot list response")
	}
}

func TestHandler_SnapshotListRespWithoutWaiterCompletesDurableCommand(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{Name: "Tokyo-1", Status: "online"}
	require.NoError(t, database.DB.Create(&agent).Error)
	commandService := commands.NewService(database, nil)
	request, err := protocol.NewMessage(protocol.TypeSnapshotListReq, protocol.SnapshotListReqPayload{AgentID: agent.ID})
	require.NoError(t, err)
	command, err := commandService.CreateCommand(t.Context(), commands.CreateCommandInput{
		AgentID: agent.ID,
		Type:    protocol.TypeSnapshotListReq,
		Message: *request,
	})
	require.NoError(t, err)
	require.NoError(t, database.DB.Model(&db.AgentCommand{}).
		Where("id = ?", command.ID).
		Update("status", commands.CommandStatusDispatched).Error)
	snapshotTime := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	response, err := protocol.NewMessage(protocol.TypeSnapshotListResp, protocol.SnapshotListRespPayload{
		AgentID: agent.ID,
		Snapshots: []protocol.SnapshotInfo{
			{ID: "snap-durable", Time: snapshotTime, Paths: []string{"/srv"}, Size: 1024},
		},
	})
	require.NoError(t, err)
	response.ID = request.ID

	hub := NewHub()
	handler := NewHandler(hub, events.NewBus(), validTestAuth, noPolicy, nil)
	handler.SnapshotListResponseProcessor = masterapi.NewSnapshotListResponseProcessor(database, commandService)
	handler.dispatch(agent.ID, *response)

	var snapshot db.Snapshot
	require.NoError(t, database.DB.First(&snapshot, "agent_id = ? AND snapshot_id = ?", agent.ID, "snap-durable").Error)
	assert.True(t, snapshot.Timestamp.Equal(snapshotTime))
	assert.JSONEq(t, `["/srv"]`, snapshot.Paths)
	assert.Equal(t, int64(1024), snapshot.Size)

	var found db.AgentCommand
	require.NoError(t, database.DB.First(&found, "id = ?", command.ID).Error)
	assert.Equal(t, commands.CommandStatusSucceeded, found.Status)
	assert.Contains(t, found.Result, `"snap-durable"`)
	assert.Empty(t, found.ErrorMessage)
	assert.NotNil(t, found.CompletedAt)
}

func TestHandler_OldConnectionCleanupDoesNotRemoveReplacementConnection(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	offlineEvents := make(chan events.Event, 1)
	setup.bus.Subscribe(events.AgentOffline, func(event events.Event) {
		offlineEvents <- event
	})
	oldConn := &SafeConn{}
	newConn := &SafeConn{}
	setup.hub.Add("agent-1", oldConn)
	setup.hub.Add("agent-1", newConn)

	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.cleanupConnection("agent-1", oldConn)

	status := setup.hub.GetAllAgents()["agent-1"]
	require.NotNil(t, status)
	assert.Same(t, newConn, status.Conn)
	assert.True(t, setup.hub.IsOnline("agent-1"))

	select {
	case event := <-offlineEvents:
		t.Fatalf("unexpected offline event for replacement connection: %#v", event)
	default:
	}
}

func TestHandler_CleanupAfterMonitorOfflineDoesNotPublishDuplicateOffline(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	conn := &SafeConn{}
	now := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)

	offlineEvents := subscribeOfflineEvents(setup.bus)
	setup.hub.Add("agent-1", conn)
	setup.hub.UpdateLastSeen("agent-1", now.Add(-2*time.Minute))

	monitor := NewMonitorWithConfig(setup.hub, setup.bus, 10*time.Millisecond, time.Minute, func() time.Time {
		return now
	})
	monitor.scan()

	handler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy, nil)
	handler.cleanupConnection("agent-1", conn)

	assert.Equal(t, []string{"agent-1"}, offlineEvents.snapshot())
	assert.NotContains(t, setup.hub.GetAllAgents(), "agent-1")
}

func TestHandler_OnlinePublishPanicStillCleansRegistration(t *testing.T) {
	bus := events.NewBus()
	bus.Subscribe(events.AgentOnline, func(events.Event) {
		panic("online subscriber failed")
	})
	hub := NewHub()
	handler := NewHandler(hub, bus, validTestAuth, noPolicy, nil)
	router := gin.New()
	router.GET("/ws", handler.HandleWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		return hub.IsOnline("agent-1")
	}, time.Second, 10*time.Millisecond)

	require.NoError(t, conn.Close())
	require.Eventually(t, func() bool {
		return !hub.IsOnline("agent-1")
	}, time.Second, 10*time.Millisecond)
}

func TestHandler_RejectsCrossOriginWebSocket(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	header := http.Header{}
	header.Set("Origin", "http://evil.example")
	conn, resp, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), header)
	if conn != nil {
		conn.Close()
	}

	require.Error(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestHandler_AllowsEmptyOriginAndSameOrigin(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)
	wsURL := websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}})

	emptyOriginConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.NoError(t, emptyOriginConn.Close())

	originURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	header := http.Header{}
	header.Set("Origin", originURL.Scheme+"://"+originURL.Host)
	sameOriginConn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	require.NoError(t, err)
	require.NoError(t, sameOriginConn.Close())
}

func TestHandler_PolicyPushedOnConnect(t *testing.T) {
	policyMsg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:    "agent-1",
		BackupDirs: []string{"/srv"},
	})
	require.NoError(t, err)

	setup := setupHandlerTest(t, validTestAuth, func(agentID string) (*protocol.Message, bool) {
		assert.Equal(t, "agent-1", agentID)
		return policyMsg, true
	})
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	var received protocol.Message
	require.NoError(t, conn.ReadJSON(&received))

	assert.Equal(t, policyMsg.Type, received.Type)
	assert.Equal(t, policyMsg.ID, received.ID)
	assert.JSONEq(t, string(policyMsg.Payload), string(received.Payload))
}

func TestHandler_FullRegistrationFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	database, err := db.New(t.TempDir())
	require.NoError(t, err)
	agent := db.Agent{
		Name:        "Tokyo-1",
		EnrollToken: "ek_full_flow",
		Status:      "offline",
	}
	require.NoError(t, database.DB.Create(&agent).Error)

	policyMsg, err := protocol.NewMessage(protocol.TypePolicyPush, protocol.PolicyPushPayload{
		AgentID:    agent.ID,
		BackupDirs: []string{"/srv"},
		Schedule:   "0 4 * * *",
	})
	require.NoError(t, err)

	hub := NewHub()
	bus := events.NewBus()
	onlineEvents := make(chan events.Event, 1)
	bus.Subscribe(events.AgentOnline, func(event events.Event) {
		onlineEvents <- event
	})

	agentHandler := masterapi.NewAgentHandler(database)
	wsHandler := NewHandler(
		hub,
		bus,
		func(token string) (string, error) {
			var enrolled db.Agent
			if err := database.DB.First(&enrolled, "agent_token = ?", token).Error; err != nil {
				return "", err
			}
			return enrolled.ID, nil
		},
		func(agentID string) (*protocol.Message, bool) {
			if agentID != agent.ID {
				return nil, false
			}
			return policyMsg, true
		},
		nil,
	)
	router := gin.New()
	router.POST("/api/agent/enroll", agentHandler.Enroll)
	router.GET("/ws/agent", wsHandler.HandleWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	cfg, err := agentenroll.Enroll(server.URL, "ek_full_flow", filepath.Join(t.TempDir(), "agent.yaml"))
	require.NoError(t, err)
	assert.Equal(t, agent.ID, cfg.AgentID)
	assert.NotEmpty(t, cfg.AgentToken)

	var stored db.Agent
	require.NoError(t, database.DB.First(&stored, "id = ?", agent.ID).Error)
	assert.Empty(t, stored.EnrollToken)
	assert.Equal(t, cfg.AgentToken, stored.AgentToken)

	conn, _, err := websocket.DefaultDialer.Dial(
		websocketURL(server.URL, "/ws/agent", url.Values{"token": []string{cfg.AgentToken}}),
		nil,
	)
	require.NoError(t, err)
	defer conn.Close()

	var received protocol.Message
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, conn.ReadJSON(&received))
	assert.Equal(t, protocol.TypePolicyPush, received.Type)
	assert.Equal(t, policyMsg.ID, received.ID)
	assert.JSONEq(t, string(policyMsg.Payload), string(received.Payload))

	payload, err := protocol.ParsePayload[protocol.PolicyPushPayload](&received)
	require.NoError(t, err)
	assert.Equal(t, agent.ID, payload.AgentID)

	require.Eventually(t, func() bool {
		return hub.IsOnline(agent.ID)
	}, time.Second, 10*time.Millisecond)

	select {
	case event := <-onlineEvents:
		assert.Equal(t, events.AgentOnline, event.Type)
		assert.Equal(t, agent.ID, event.Payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for agent online event")
	}
}

func TestHandler_PublishesAgentOnlineAndOfflineEvents(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	eventsCh := make(chan events.Event, 2)
	setup.bus.Subscribe(events.AgentOnline, func(event events.Event) {
		eventsCh <- event
	})
	setup.bus.Subscribe(events.AgentOffline, func(event events.Event) {
		eventsCh <- event
	})
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)

	select {
	case online := <-eventsCh:
		assert.Equal(t, events.AgentOnline, online.Type)
		assert.Equal(t, "agent-1", online.Payload)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for agent online event")
	}

	require.NoError(t, conn.Close())

	require.Eventually(t, func() bool {
		select {
		case offline := <-eventsCh:
			return offline.Type == events.AgentOffline && offline.Payload == "agent-1"
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
}
