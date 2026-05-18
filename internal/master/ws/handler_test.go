package ws

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	handler := NewHandler(hub, bus, auth, lookup)
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

func TestHandler_HeartbeatDispatchUpdatesLastSeen(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	server := httptest.NewServer(setup.router)
	t.Cleanup(server.Close)

	fixedNow := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)
	originalTimeNow := timeNow
	timeNow = func() time.Time { return fixedNow }
	t.Cleanup(func() {
		timeNow = originalTimeNow
	})

	conn, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL, "/ws", url.Values{"token": []string{"valid-token"}}), nil)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.WriteJSON(protocol.Message{Type: protocol.TypeHeartbeat}))

	require.Eventually(t, func() bool {
		status := setup.hub.GetAllAgents()["agent-1"]
		return status != nil && status.LastSeenAt.Equal(fixedNow)
	}, time.Second, 10*time.Millisecond)
}

func TestHandler_PolicyAckDispatchPublishesPolicyChanged(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	eventsCh := make(chan events.Event, 1)
	setup.bus.Subscribe(events.PolicyChanged, func(event events.Event) {
		eventsCh <- event
	})

	setup.hub.Add("agent-1", &SafeConn{})
	setupHandler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy)
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

func TestHandler_TaskResultDispatchPublishesRawPayload(t *testing.T) {
	setup := setupHandlerTest(t, validTestAuth, noPolicy)
	eventsCh := make(chan events.Event, 1)
	setup.bus.Subscribe(events.EventType(protocol.TypeTaskResult), func(event events.Event) {
		eventsCh <- event
	})
	rawPayload := json.RawMessage(`{"task_type":"backup","status":"success"}`)

	setupHandler := NewHandler(setup.hub, setup.bus, validTestAuth, noPolicy)
	setupHandler.dispatch("agent-1", protocol.Message{
		Type:    protocol.TypeTaskResult,
		Payload: rawPayload,
	})

	select {
	case event := <-eventsCh:
		assert.Equal(t, events.EventType(protocol.TypeTaskResult), event.Type)
		payload, ok := event.Payload.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "agent-1", payload["agent_id"])
		assert.Equal(t, rawPayload, payload["payload"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task result event")
	}
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
