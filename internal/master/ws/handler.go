package ws

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

type AgentAuthFunc func(token string) (agentID string, err error)
type PolicyLookupFunc func(agentID string) (*protocol.Message, bool)

type Handler struct {
	hub          *Hub
	eventBus     *events.Bus
	authAgent    AgentAuthFunc
	policyLookup PolicyLookupFunc
	upgrader     websocket.Upgrader
}

var timeNow = time.Now

func NewHandler(hub *Hub, eventBus *events.Bus, authAgent AgentAuthFunc, policyLookup PolicyLookupFunc) *Handler {
	return &Handler{
		hub:          hub,
		eventBus:     eventBus,
		authAgent:    authAgent,
		policyLookup: policyLookup,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool {
				return true
			},
		},
	}
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
	h.eventBus.Publish(events.Event{Type: events.AgentOnline, Payload: agentID})
	defer func() {
		h.hub.Remove(agentID)
		h.eventBus.Publish(events.Event{Type: events.AgentOffline, Payload: agentID})
	}()

	if h.policyLookup != nil {
		if msg, ok := h.policyLookup(agentID); ok && msg != nil {
			if err := safeConn.WriteJSON(msg); err != nil {
				return
			}
		}
	}

	h.readLoop(agentID, safeConn)
}

func (h *Handler) readLoop(agentID string, conn *SafeConn) {
	for {
		var msg protocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		h.dispatch(agentID, msg)
	}
}

func (h *Handler) dispatch(agentID string, msg protocol.Message) {
	switch msg.Type {
	case protocol.TypeHeartbeat:
		h.hub.UpdateLastSeen(agentID, timeNow())
	case protocol.TypePolicyAck:
		h.eventBus.Publish(events.Event{
			Type: events.PolicyChanged,
			Payload: map[string]interface{}{
				"agent_id": agentID,
				"action":   "ack",
			},
		})
	case protocol.TypeTaskResult:
		h.eventBus.Publish(events.Event{
			Type: events.EventType(protocol.TypeTaskResult),
			Payload: map[string]interface{}{
				"agent_id": agentID,
				"payload":  msg.Payload,
			},
		})
	}
}
