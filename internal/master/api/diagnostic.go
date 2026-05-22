package api

import (
	"archive/zip"
	"encoding/json"
	"fmt"
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

	h.writeMeta(zw)
	h.writeMasterLogs(zw)
	h.writeNodes(zw)
	h.writeStorage(zw)
	h.writePolicies(zw)
	h.writeRecentErrors(zw)
	h.collectAgentLogs(zw, agentIDs)
}

func parseAgentIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	ids := make([]string, 0)
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *DiagnosticHandler) writeMeta(zw *zip.Writer) {
	writeJSONFile(zw, "meta.json", map[string]any{
		"version":      h.Version,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
	})
}

func (h *DiagnosticHandler) writeMasterLogs(zw *zip.Writer) {
	if h.LogBuf == nil {
		return
	}
	writeTextFile(zw, "master/logs.txt", redact.Text(string(h.LogBuf.Bytes())))
}

func (h *DiagnosticHandler) writeNodes(zw *zip.Writer) {
	var agents []db.Agent
	if err := h.DB.DB.Find(&agents).Error; err != nil {
		log.Printf("diagnostic: query agents failed: %v", err)
		return
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
	writeJSONFile(zw, "master/nodes.json", nodes)
}

func (h *DiagnosticHandler) writeStorage(zw *zip.Writer) {
	var configs []db.StorageConfig
	if err := h.DB.DB.Find(&configs).Error; err != nil {
		log.Printf("diagnostic: query storage failed: %v", err)
		return
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
	writeJSONFile(zw, "master/storage.json", items)
}

func (h *DiagnosticHandler) writePolicies(zw *zip.Writer) {
	var policies []db.BackupPolicy
	if err := h.DB.DB.Find(&policies).Error; err != nil {
		log.Printf("diagnostic: query policies failed: %v", err)
		return
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
	writeJSONFile(zw, "master/policies.json", items)
}

func (h *DiagnosticHandler) writeRecentErrors(zw *zip.Writer) {
	var tasks []db.TaskHistory
	if err := h.DB.DB.Where("status = ?", "failed").
		Order("created_at DESC").
		Limit(50).
		Find(&tasks).Error; err != nil {
		log.Printf("diagnostic: query failed tasks failed: %v", err)
		return
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
	writeJSONFile(zw, "master/recent_errors.json", items)
}

func (h *DiagnosticHandler) collectAgentLogs(zw *zip.Writer, agentIDs []string) {
	if h.Hub == nil || len(agentIDs) == 0 {
		return
	}

	agentNames := h.loadAgentNames(agentIDs)
	for _, agentID := range agentIDs {
		name := agentNames[agentID]
		if name == "" {
			name = agentID
		}
		dirName := fmt.Sprintf("agents/%s", name)

		if !h.Hub.IsOnline(agentID) {
			writeTextFile(zw, dirName+"/error.txt", "agent offline at collection time")
			continue
		}

		msg, err := protocol.NewMessage(protocol.TypeCollectLogsReq, protocol.CollectLogsReqPayload{
			MaxBytes: 5 * 1024 * 1024,
		})
		if err != nil {
			writeTextFile(zw, dirName+"/error.txt", fmt.Sprintf("create message failed: %v", err))
			continue
		}

		respCh, err := h.Hub.SendAndWait(agentID, *msg, 30*time.Second)
		if err != nil {
			writeTextFile(zw, dirName+"/error.txt", fmt.Sprintf("send failed: %v", err))
			continue
		}

		resp, ok := <-respCh
		if !ok {
			writeTextFile(zw, dirName+"/timeout.txt", "agent did not respond within 30 seconds")
			continue
		}

		payload, err := protocol.ParsePayload[protocol.CollectLogsRespPayload](&resp)
		if err != nil {
			writeTextFile(zw, dirName+"/error.txt", fmt.Sprintf("parse response failed: %v", err))
			continue
		}
		if payload.Error != "" {
			writeTextFile(zw, dirName+"/error.txt", payload.Error)
		}
		if payload.Logs != "" {
			writeTextFile(zw, dirName+"/logs.txt", payload.Logs)
		}
	}
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

func writeJSONFile(zw *zip.Writer, name string, data any) {
	w, err := zw.Create(name)
	if err != nil {
		log.Printf("diagnostic: create zip entry %s failed: %v", name, err)
		return
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(data); err != nil {
		log.Printf("diagnostic: write zip entry %s failed: %v", name, err)
	}
}

func writeTextFile(zw *zip.Writer, name string, content string) {
	w, err := zw.Create(name)
	if err != nil {
		log.Printf("diagnostic: create zip entry %s failed: %v", name, err)
		return
	}
	if _, err := w.Write([]byte(content)); err != nil {
		log.Printf("diagnostic: write zip entry %s failed: %v", name, err)
	}
}
