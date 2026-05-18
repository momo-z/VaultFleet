package ws

import (
	"errors"
	"sync"
	"time"
)

var ErrAgentNotConnected = errors.New("agent not connected")

type AgentStatus struct {
	Conn       *SafeConn
	Online     bool
	LastSeenAt time.Time
}

type Hub struct {
	mu     sync.RWMutex
	agents map[string]*AgentStatus
}

func NewHub() *Hub {
	return &Hub{
		agents: make(map[string]*AgentStatus),
	}
}

func (h *Hub) Add(agentID string, conn *SafeConn) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.agents[agentID] = &AgentStatus{
		Conn:       conn,
		Online:     true,
		LastSeenAt: time.Now(),
	}
}

func (h *Hub) Remove(agentID string) {
	h.mu.Lock()
	status, ok := h.agents[agentID]
	if ok {
		status.Online = false
		delete(h.agents, agentID)
	}
	h.mu.Unlock()

	if ok && status.Conn != nil {
		_ = status.Conn.Close()
	}
}

func (h *Hub) Send(agentID string, msg interface{}) error {
	h.mu.RLock()
	status, ok := h.agents[agentID]
	h.mu.RUnlock()

	if !ok || status == nil || !status.Online || status.Conn == nil {
		return ErrAgentNotConnected
	}

	return status.Conn.WriteJSON(msg)
}

func (h *Hub) IsOnline(agentID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	status, ok := h.agents[agentID]
	return ok && status != nil && status.Online
}

func (h *Hub) UpdateLastSeen(agentID string, t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if status, ok := h.agents[agentID]; ok && status != nil {
		status.LastSeenAt = t
	}
}

func (h *Hub) GetAllAgents() map[string]*AgentStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	agents := make(map[string]*AgentStatus, len(h.agents))
	for agentID, status := range h.agents {
		if status == nil {
			agents[agentID] = nil
			continue
		}
		statusCopy := *status
		agents[agentID] = &statusCopy
	}
	return agents
}
