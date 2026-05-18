package ws

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHub_AddAndRemove(t *testing.T) {
	hub := NewHub()
	conn := &SafeConn{}

	beforeAdd := time.Now()
	hub.Add("agent-1", conn)

	agents := hub.GetAllAgents()
	status, ok := agents["agent-1"]
	require.True(t, ok)
	assert.Same(t, conn, status.Conn)
	assert.True(t, status.Online)
	assert.False(t, status.LastSeenAt.Before(beforeAdd))
	assert.True(t, hub.IsOnline("agent-1"))

	hub.Remove("agent-1")

	assert.False(t, hub.IsOnline("agent-1"))
	assert.NotContains(t, hub.GetAllAgents(), "agent-1")
}

func TestHub_SendToOnlineAgent(t *testing.T) {
	hub := NewHub()

	err := hub.Send("missing-agent", map[string]string{"type": "heartbeat"})

	assert.ErrorIs(t, err, ErrAgentNotConnected)
}

func TestHub_UpdateLastSeen(t *testing.T) {
	hub := NewHub()
	conn := &SafeConn{}
	initial := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 5, 18, 9, 30, 0, 0, time.UTC)

	hub.Add("agent-1", conn)
	hub.UpdateLastSeen("agent-1", initial)
	hub.UpdateLastSeen("agent-1", updated)
	hub.UpdateLastSeen("missing-agent", time.Now())

	status := hub.GetAllAgents()["agent-1"]
	require.NotNil(t, status)
	assert.Equal(t, updated, status.LastSeenAt)
}

func TestHub_GetAllAgents(t *testing.T) {
	hub := NewHub()
	firstConn := &SafeConn{}
	secondConn := &SafeConn{}

	hub.Add("agent-1", firstConn)
	hub.Add("agent-2", secondConn)

	agents := hub.GetAllAgents()

	require.Len(t, agents, 2)
	assert.Same(t, firstConn, agents["agent-1"].Conn)
	assert.Same(t, secondConn, agents["agent-2"].Conn)
	assert.True(t, agents["agent-1"].Online)
	assert.True(t, agents["agent-2"].Online)
}

func TestHub_GetAllAgentsReturnsStatusCopy(t *testing.T) {
	hub := NewHub()
	hub.Add("agent-1", &SafeConn{})

	agents := hub.GetAllAgents()
	require.Contains(t, agents, "agent-1")

	agents["agent-1"].Online = false
	delete(agents, "agent-1")

	assert.True(t, hub.IsOnline("agent-1"))
	assert.Contains(t, hub.GetAllAgents(), "agent-1")
}
