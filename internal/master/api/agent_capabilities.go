package api

import (
	"encoding/json"

	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

func agentHasCapability(database *db.Database, agentID string, capability string) (bool, error) {
	var agent db.Agent
	if err := database.DB.Select("system_info").First(&agent, "id = ?", agentID).Error; err != nil {
		return false, err
	}
	return systemInfoHasCapability(agent.SystemInfo, capability), nil
}

func agentSupportsPlaintextRclonePass(database *db.Database, agentID string) (bool, error) {
	return agentHasCapability(database, agentID, protocol.CapabilityPolicyPlaintextRclonePass)
}

func systemInfoHasCapability(raw string, capability string) bool {
	info := parseAgentSystemInfoMap(raw)
	capabilities, ok := info["capabilities"].([]any)
	if !ok {
		return false
	}
	for _, value := range capabilities {
		if text, ok := value.(string); ok && text == capability {
			return true
		}
	}
	return false
}

func mergeHeartbeatIntoSystemInfo(raw string, heartbeat *protocol.HeartbeatPayload) string {
	info := parseAgentSystemInfoMap(raw)
	if heartbeat != nil {
		if heartbeat.AgentVersion != "" {
			info["version"] = heartbeat.AgentVersion
		}
		if len(heartbeat.Capabilities) > 0 {
			info["capabilities"] = heartbeat.Capabilities
		}
	}
	data, err := json.Marshal(info)
	if err != nil {
		return raw
	}
	return string(data)
}

func parseAgentSystemInfoMap(raw string) map[string]any {
	info := make(map[string]any)
	if raw == "" {
		return info
	}
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return make(map[string]any)
	}
	return info
}
