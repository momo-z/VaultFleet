package ws

import (
	"context"
	"log"
	"time"

	"vaultfleet/internal/master/events"
)

const (
	MonitorInterval  = 10 * time.Second
	OfflineThreshold = 60 * time.Second
)

type Monitor struct {
	hub       *Hub
	eventBus  *events.Bus
	interval  time.Duration
	threshold time.Duration
	nowFunc   func() time.Time
}

func NewMonitor(hub *Hub, eventBus *events.Bus) *Monitor {
	return NewMonitorWithConfig(hub, eventBus, MonitorInterval, OfflineThreshold, time.Now)
}

func NewMonitorWithConfig(hub *Hub, eventBus *events.Bus, interval, threshold time.Duration, nowFunc func() time.Time) *Monitor {
	if nowFunc == nil {
		nowFunc = time.Now
	}

	return &Monitor{
		hub:       hub,
		eventBus:  eventBus,
		interval:  interval,
		threshold: threshold,
		nowFunc:   nowFunc,
	}
}

func (m *Monitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.scan()
		}
	}
}

func (m *Monitor) scan() {
	now := m.nowFunc()
	agents := m.hub.GetAllAgents()

	for agentID, status := range agents {
		if status == nil || !status.Online {
			continue
		}
		if now.Sub(status.LastSeenAt) <= m.threshold {
			continue
		}
		if !m.hub.MarkOfflineIfStale(agentID, now, m.threshold) {
			continue
		}

		log.Printf("agent %s offline: last seen %v ago", agentID, now.Sub(status.LastSeenAt))
		m.eventBus.Publish(events.Event{
			Type:    events.AgentOffline,
			Payload: agentID,
		})
	}
}
