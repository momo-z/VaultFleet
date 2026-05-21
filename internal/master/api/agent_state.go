package api

import (
	"encoding/json"
	"time"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/events"
	"vaultfleet/pkg/protocol"
)

type AgentStateUpdater func(agentID string, status string, lastSeenAt *time.Time) error

func NewAgentStateUpdater(database *db.Database) AgentStateUpdater {
	return func(agentID string, status string, lastSeenAt *time.Time) error {
		if database == nil || database.DB == nil || agentID == "" || status == "" {
			return nil
		}
		updates := map[string]any{"status": status}
		if lastSeenAt != nil {
			updates["last_seen_at"] = *lastSeenAt
		}
		return database.DB.Model(&db.Agent{}).Where("id = ?", agentID).Updates(updates).Error
	}
}

type HeartbeatStateUpdater func(agentID string, status string, lastSeenAt *time.Time, heartbeat *protocol.HeartbeatPayload) error

func NewHeartbeatStateUpdater(database *db.Database) HeartbeatStateUpdater {
	return func(agentID string, status string, lastSeenAt *time.Time, heartbeat *protocol.HeartbeatPayload) error {
		if database == nil || database.DB == nil || agentID == "" || status == "" {
			return nil
		}
		updates := map[string]any{"status": status}
		if lastSeenAt != nil {
			updates["last_seen_at"] = *lastSeenAt
		}
		if heartbeat != nil && heartbeat.AgentVersion != "" {
			var agent db.Agent
			if err := database.DB.Select("system_info").First(&agent, "id = ?", agentID).Error; err == nil {
				updated := mergeVersionIntoSystemInfo(agent.SystemInfo, heartbeat.AgentVersion)
				updates["system_info"] = updated
			}
		}
		return database.DB.Model(&db.Agent{}).Where("id = ?", agentID).Updates(updates).Error
	}
}

func mergeVersionIntoSystemInfo(raw string, version string) string {
	var info map[string]interface{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			info = make(map[string]interface{})
		}
	} else {
		info = make(map[string]interface{})
	}
	info["version"] = version
	data, err := json.Marshal(info)
	if err != nil {
		return raw
	}
	return string(data)
}

func SubscribeAgentStateEvents(database *db.Database, bus *events.Bus) {
	if bus == nil {
		return
	}
	updater := NewAgentStateUpdater(database)
	bus.Subscribe(events.AgentOffline, func(event events.Event) {
		agentID := eventAgentID(event.Payload)
		if agentID == "" {
			return
		}
		_ = updater(agentID, "offline", nil)
	})
}

func eventAgentID(payload any) string {
	switch value := payload.(type) {
	case string:
		return value
	case map[string]any:
		if agentID, ok := value["agent_id"].(string); ok {
			return agentID
		}
		if agentID, ok := value["id"].(string); ok {
			return agentID
		}
	}
	return ""
}
