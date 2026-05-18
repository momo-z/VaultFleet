package ws

import (
	"sync"
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

func TestHub_MarkOnlineAndMarkOffline(t *testing.T) {
	hub := NewHub()
	hub.Add("agent-1", &SafeConn{})

	assert.True(t, hub.MarkOffline("agent-1"))
	assert.False(t, hub.IsOnline("agent-1"))

	assert.False(t, hub.MarkOffline("agent-1"))
	assert.True(t, hub.MarkOnline("agent-1"))
	assert.True(t, hub.IsOnline("agent-1"))

	assert.False(t, hub.MarkOnline("agent-1"))
	assert.False(t, hub.MarkOffline("missing-agent"))
	assert.False(t, hub.MarkOnline("missing-agent"))
}

func TestHub_MarkOfflineIfStaleRechecksCurrentLastSeen(t *testing.T) {
	hub := NewHub()
	now := time.Date(2026, 5, 18, 10, 0, 0, 0, time.UTC)

	hub.Add("agent-1", &SafeConn{})
	hub.UpdateLastSeen("agent-1", now.Add(-2*time.Minute))

	snapshot := hub.GetAllAgents()["agent-1"]
	require.NotNil(t, snapshot)
	require.True(t, now.Sub(snapshot.LastSeenAt) > time.Minute)

	hub.UpdateLastSeen("agent-1", now)

	assert.False(t, hub.MarkOfflineIfStale("agent-1", now, time.Minute))
	assert.True(t, hub.IsOnline("agent-1"))

	hub.UpdateLastSeen("agent-1", now.Add(-2*time.Minute))
	assert.True(t, hub.MarkOfflineIfStale("agent-1", now, time.Minute))
	assert.False(t, hub.IsOnline("agent-1"))
	assert.False(t, hub.MarkOfflineIfStale("agent-1", now, time.Minute))
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

func TestHub_AddReplacesAndClosesPreviousConnection(t *testing.T) {
	hub := NewHub()
	oldConn := &SafeConn{}
	newConn := &SafeConn{}

	hub.Add("agent-1", oldConn)
	hub.Add("agent-1", newConn)

	assert.ErrorIs(t, oldConn.WriteJSON(map[string]string{"type": "heartbeat"}), ErrNilConn)
	status := hub.GetAllAgents()["agent-1"]
	require.NotNil(t, status)
	assert.Same(t, newConn, status.Conn)
	assert.True(t, hub.IsOnline("agent-1"))
}

func TestHub_RemoveIfCurrentDoesNotRemoveReplacementConnection(t *testing.T) {
	hub := NewHub()
	oldConn := &SafeConn{}
	newConn := &SafeConn{}

	hub.Add("agent-1", oldConn)
	hub.Add("agent-1", newConn)
	hub.RemoveIfCurrent("agent-1", oldConn)

	status := hub.GetAllAgents()["agent-1"]
	require.NotNil(t, status)
	assert.Same(t, newConn, status.Conn)
	assert.True(t, hub.IsOnline("agent-1"))

	hub.RemoveIfCurrent("agent-1", newConn)

	assert.False(t, hub.IsOnline("agent-1"))
	assert.NotContains(t, hub.GetAllAgents(), "agent-1")
}

func TestHub_SendConcurrentWithRemoveIsRaceFree(t *testing.T) {
	hub := NewHub()
	hub.Add("agent-1", &SafeConn{})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_ = hub.Send("agent-1", map[string]string{"type": "heartbeat"})
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for j := 0; j < 100; j++ {
			hub.Remove("agent-1")
			hub.Add("agent-1", &SafeConn{})
		}
	}()

	close(start)
	wg.Wait()
}
