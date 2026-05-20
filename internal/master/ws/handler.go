package ws

import (
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

const (
	maxMessageBytes = 1 << 20
	defaultPongWait = 60 * time.Second
)

type AgentAuthFunc func(token string) (agentID string, err error)
type PolicyLookupFunc func(agentID string) (*protocol.Message, bool)
type TaskResultProcessorFunc func(agentID string, msg protocol.Message) error
type PolicyAckProcessorFunc func(agentID string, msg protocol.Message) error
type SnapshotListResponseProcessorFunc func(agentID string, msg protocol.Message) error
type PendingCommandDispatcherFunc func(agentID string) error
type Handler struct {
	hub                           *Hub
	eventBus                      *events.Bus
	authAgent                     AgentAuthFunc
	policyLookup                  PolicyLookupFunc
	taskResultProcess             TaskResultProcessorFunc
	PolicyAckProcessor            PolicyAckProcessorFunc
	SnapshotListResponseProcessor SnapshotListResponseProcessorFunc
	PendingCommandDispatcher      PendingCommandDispatcherFunc
	AgentStateUpdater             func(agentID string, status string, lastSeenAt *time.Time) error
	upgrader                      websocket.Upgrader
	now                           func() time.Time
	pongWait                      time.Duration
}

func NewHandler(hub *Hub, eventBus *events.Bus, authAgent AgentAuthFunc, policyLookup PolicyLookupFunc, taskResultProcess TaskResultProcessorFunc) *Handler {
	return &Handler{
		hub:               hub,
		eventBus:          eventBus,
		authAgent:         authAgent,
		policyLookup:      policyLookup,
		taskResultProcess: taskResultProcess,
		now:               time.Now,
		pongWait:          defaultPongWait,
		upgrader: websocket.Upgrader{
			CheckOrigin: allowAgentOrigin,
		},
	}
}

func allowAgentOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	originURL, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return originURL.Host == r.Host
}

func (h *Handler) HandleWebSocket(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "missing token"})
		return
	}

	agentID, err := h.authAgent(token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"ok": false, "error": "invalid token"})
		return
	}

	conn, err := h.upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}

	safeConn := NewSafeConn(conn)
	h.hub.Add(agentID, safeConn)
	now := h.now()
	h.hub.UpdateLastSeen(agentID, now)
	h.updateAgentState(agentID, "online", &now)
	defer h.cleanupConnection(agentID, safeConn)
	h.eventBus.Publish(events.Event{Type: events.AgentOnline, Payload: agentID})

	if h.policyLookup != nil {
		if msg, ok := h.policyLookup(agentID); ok && msg != nil {
			if err := safeConn.WriteJSON(msg); err != nil {
				return
			}
		}
	}

	if h.PendingCommandDispatcher != nil {
		if err := h.PendingCommandDispatcher(agentID); err != nil {
			log.Printf("dispatch pending commands for agent %s failed: %v", agentID, err)
		}
	}

	h.readLoop(agentID, safeConn)
}

func (h *Handler) cleanupConnection(agentID string, conn *SafeConn) {
	if h.hub.RemoveIfCurrent(agentID, conn) {
		h.updateAgentState(agentID, "offline", nil)
		h.eventBus.Publish(events.Event{Type: events.AgentOffline, Payload: agentID})
	}
}

func (h *Handler) readLoop(agentID string, conn *SafeConn) {
	conn.SetReadLimit(maxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(h.pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(h.pongWait))
	})

	for {
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(h.pongWait))
		h.dispatch(agentID, msg)
	}
}

func (h *Handler) dispatch(agentID string, msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeHeartbeat:
		now := h.now()
		h.hub.UpdateLastSeen(agentID, now)
		h.hub.MarkOnline(agentID)
		h.updateAgentState(agentID, "online", &now)
	case protocol.TypePolicyAck:
		if h.PolicyAckProcessor != nil {
			if err := h.PolicyAckProcessor(agentID, msg); err != nil {
				log.Printf("process policy ack failed for agent %s: %v", agentID, err)
			}
		}
		h.eventBus.Publish(events.Event{
			Type: events.PolicyChanged,
			Payload: map[string]interface{}{
				"agent_id": agentID,
				"action":   "ack",
			},
		})
	case protocol.TypeTaskResult:
		if h.taskResultProcess != nil {
			if err := h.taskResultProcess(agentID, msg); err != nil {
				log.Printf("process task result failed for agent %s: %v", agentID, err)
				return
			}
		}
		h.eventBus.Publish(events.Event{
			Type: events.TaskResult,
			Payload: map[string]interface{}{
				"agent_id": agentID,
				"payload":  msg.Payload,
			},
		})
	case protocol.TypeDirBrowseResp, protocol.TypeSnapshotListResp:
		handled := h.hub.HandleResponse(agentID, msg)
		if !handled && msg.Type == protocol.TypeSnapshotListResp && h.SnapshotListResponseProcessor != nil {
			if err := h.SnapshotListResponseProcessor(agentID, msg); err != nil {
				log.Printf("process snapshot list response failed for agent %s: %v", agentID, err)
			}
		}
	}
}

func (h *Handler) updateAgentState(agentID string, status string, lastSeenAt *time.Time) {
	if h.AgentStateUpdater == nil {
		return
	}
	if err := h.AgentStateUpdater(agentID, status, lastSeenAt); err != nil {
		log.Printf("update agent %s state failed: %v", agentID, err)
	}
}
