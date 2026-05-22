package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"vaultfleet/internal/master/db"
	"vaultfleet/internal/master/logbuf"
	"vaultfleet/pkg/protocol"
	"vaultfleet/pkg/redact"
)

type DiagnosticHub interface {
	SendAndWait(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error)
	IsOnline(agentID string) bool
}

type DiagnosticHandler struct {
	DB      *db.Database
	Hub     DiagnosticHub
	LogBuf  *logbuf.RingBuffer
	Version string
}

func NewDiagnosticHandler(database *db.Database, hub DiagnosticHub, logBuf *logbuf.RingBuffer) *DiagnosticHandler {
	return &DiagnosticHandler{DB: database, Hub: hub, LogBuf: logBuf}
}

func RegisterDiagnosticRoutes(rg *gin.RouterGroup, h *DiagnosticHandler) {
	rg.GET("/diagnostic", h.Generate)
}

func (h *DiagnosticHandler) Generate(c *gin.Context) {
	agentIDs := parseAgentIDs(c.Query("agents"))

	filename := fmt.Sprintf("vaultfleet-diagnostic-%s.zip", time.Now().Format("20060102T150405"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Content-Type", "application/zip")
	c.Status(http.StatusOK)

	zw := zip.NewWriter(c.Writer)
	defer zw.Close()

	warnings := make([]string, 0)
	recordWarning := func(err error) {
		if err == nil {
			return
		}
		text := redact.Text(err.Error())
		log.Printf("diagnostic: %s", text)
		warnings = append(warnings, text)
	}

	recordWarning(h.writeMasterLogs(zw))
	recordWarning(h.writeNodes(zw))
	recordWarning(h.writeStorage(zw))
	recordWarning(h.writePolicies(zw))
	recordWarning(h.writeRecentErrors(zw))
	warnings = append(warnings, h.collectAgentLogs(zw, agentIDs)...)
	h.writeMeta(zw, warnings)
}

func parseAgentIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *DiagnosticHandler) writeMeta(zw *zip.Writer, warnings []string) {
	meta := map[string]any{
		"version":      h.Version,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
	}
	if len(warnings) > 0 {
		meta["warnings"] = warnings
	}
	if err := writeJSONFile(zw, "meta.json", meta); err != nil {
		log.Printf("diagnostic: write meta failed: %v", err)
	}
}

func (h *DiagnosticHandler) writeMasterLogs(zw *zip.Writer) error {
	if h.LogBuf == nil {
		return nil
	}
	return writeTextFile(zw, "master/logs.txt", redact.Text(string(h.LogBuf.Bytes())))
}

func (h *DiagnosticHandler) writeNodes(zw *zip.Writer) error {
	var agents []db.Agent
	if err := h.DB.DB.Find(&agents).Error; err != nil {
		return fmt.Errorf("query agents failed: %w", err)
	}

	type nodeInfo struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		Status     string     `json:"status"`
		LastSeenAt *time.Time `json:"last_seen_at"`
		SystemInfo string     `json:"system_info,omitempty"`
	}
	nodes := make([]nodeInfo, 0, len(agents))
	for _, agent := range agents {
		nodes = append(nodes, nodeInfo{
			ID:         agent.ID,
			Name:       agent.Name,
			Status:     agent.Status,
			LastSeenAt: agent.LastSeenAt,
			SystemInfo: agent.SystemInfo,
		})
	}
	return writeJSONFile(zw, "master/nodes.json", nodes)
}

func (h *DiagnosticHandler) writeStorage(zw *zip.Writer) error {
	var configs []db.StorageConfig
	if err := h.DB.DB.Find(&configs).Error; err != nil {
		return fmt.Errorf("query storage failed: %w", err)
	}

	type storageInfo struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		RcloneType string `json:"rclone_type"`
	}
	items := make([]storageInfo, 0, len(configs))
	for _, config := range configs {
		items = append(items, storageInfo{
			ID:         config.ID,
			Name:       config.Name,
			RcloneType: config.RcloneType,
		})
	}
	return writeJSONFile(zw, "master/storage.json", items)
}

func (h *DiagnosticHandler) writePolicies(zw *zip.Writer) error {
	var policies []db.BackupPolicy
	if err := h.DB.DB.Find(&policies).Error; err != nil {
		return fmt.Errorf("query policies failed: %w", err)
	}

	type policyInfo struct {
		ID        string `json:"id"`
		AgentID   string `json:"agent_id"`
		StorageID string `json:"storage_id"`
		RepoPath  string `json:"repo_path"`
		Schedule  string `json:"schedule"`
		Synced    bool   `json:"synced"`
	}
	items := make([]policyInfo, 0, len(policies))
	for _, policy := range policies {
		items = append(items, policyInfo{
			ID:        policy.ID,
			AgentID:   policy.AgentID,
			StorageID: policy.StorageID,
			RepoPath:  policy.RepoPath,
			Schedule:  policy.Schedule,
			Synced:    policy.Synced,
		})
	}
	return writeJSONFile(zw, "master/policies.json", items)
}

func (h *DiagnosticHandler) writeRecentErrors(zw *zip.Writer) error {
	var tasks []db.TaskHistory
	if err := h.DB.DB.Where("status = ?", "failed").
		Order("created_at DESC").
		Limit(50).
		Find(&tasks).Error; err != nil {
		return fmt.Errorf("query failed tasks failed: %w", err)
	}

	type errorInfo struct {
		ID         string     `json:"id"`
		AgentID    string     `json:"agent_id"`
		Type       string     `json:"type"`
		ErrorLog   string     `json:"error_log"`
		CreatedAt  time.Time  `json:"created_at"`
		FinishedAt *time.Time `json:"finished_at"`
	}
	items := make([]errorInfo, 0, len(tasks))
	for _, task := range tasks {
		items = append(items, errorInfo{
			ID:         task.ID,
			AgentID:    task.AgentID,
			Type:       task.Type,
			ErrorLog:   redact.Text(task.ErrorLog),
			CreatedAt:  task.CreatedAt,
			FinishedAt: task.FinishedAt,
		})
	}
	return writeJSONFile(zw, "master/recent_errors.json", items)
}

func (h *DiagnosticHandler) collectAgentLogs(zw *zip.Writer, agentIDs []string) []string {
	warnings := make([]string, 0)
	if h.Hub == nil || len(agentIDs) == 0 {
		return warnings
	}

	agentNames := h.loadAgentNames(agentIDs)
	for _, agentID := range agentIDs {
		name := agentNames[agentID]
		dirName := fmt.Sprintf("agents/%s", safeArchiveSegment(agentID))
		recordAgentFailure := func(message string) {
			message = redact.Text(message)
			warnings = append(warnings, agentWarning(agentID, name, message))
			if err := writeTextFile(zw, dirName+"/error.txt", message); err != nil {
				warnings = append(warnings, agentWarning(agentID, name, err.Error()))
			}
		}

		if !h.Hub.IsOnline(agentID) {
			recordAgentFailure("agent offline at collection time")
			continue
		}

		msg, err := protocol.NewMessage(protocol.TypeCollectLogsReq, protocol.CollectLogsReqPayload{
			MaxBytes: 5 * 1024 * 1024,
		})
		if err != nil {
			recordAgentFailure(fmt.Sprintf("create message failed: %v", err))
			continue
		}

		respCh, err := h.Hub.SendAndWait(agentID, *msg, 30*time.Second)
		if err != nil {
			recordAgentFailure(fmt.Sprintf("send failed: %v", err))
			continue
		}

		resp, ok := <-respCh
		if !ok {
			message := "agent did not respond within 30 seconds"
			warnings = append(warnings, agentWarning(agentID, name, message))
			if err := writeTextFile(zw, dirName+"/timeout.txt", message); err != nil {
				warnings = append(warnings, agentWarning(agentID, name, err.Error()))
			}
			continue
		}

		payload, err := protocol.ParsePayload[protocol.CollectLogsRespPayload](&resp)
		if err != nil {
			recordAgentFailure(fmt.Sprintf("parse response failed: %v", err))
			continue
		}
		if payload.Error != "" {
			recordAgentFailure(payload.Error)
		}
		if payload.Logs != "" {
			if err := writeTextFile(zw, dirName+"/logs.txt", redact.Text(payload.Logs)); err != nil {
				warnings = append(warnings, agentWarning(agentID, name, err.Error()))
			}
		}
	}
	return warnings
}

func (h *DiagnosticHandler) loadAgentNames(agentIDs []string) map[string]string {
	names := make(map[string]string, len(agentIDs))
	var agents []db.Agent
	if err := h.DB.DB.Where("id IN ?", agentIDs).Find(&agents).Error; err != nil {
		return names
	}
	for _, agent := range agents {
		names[agent.ID] = agent.Name
	}
	return names
}

func safeArchiveSegment(segment string) string {
	var b strings.Builder
	for _, r := range segment {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func agentWarning(agentID string, name string, message string) string {
	if name == "" || name == agentID {
		return fmt.Sprintf("agent %s: %s", agentID, message)
	}
	return fmt.Sprintf("agent %s (%s): %s", agentID, name, message)
}

func writeJSONFile(zw *zip.Writer, name string, data any) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", name, err)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}
	return nil
}

func writeTextFile(zw *zip.Writer, name string, content string) error {
	w, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", name, err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		return fmt.Errorf("write zip entry %s: %w", name, err)
	}
	return nil
}
