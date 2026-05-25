package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"vaultfleet/internal/master/db"
	"vaultfleet/pkg/protocol"
)

const snapshotBrowseTimeout = 60 * time.Second

type SnapshotBrowseHandler struct {
	DB  *db.Database
	Hub BrowseHub

	timeout     time.Duration
	sendAndWait func(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error)
}

type snapshotBrowseRequest struct {
	SnapshotID string `json:"snapshot_id" binding:"required"`
	Path       string `json:"path"`
}

func NewSnapshotBrowseHandler(database *db.Database, hub BrowseHub) *SnapshotBrowseHandler {
	handler := &SnapshotBrowseHandler{
		DB:      database,
		Hub:     hub,
		timeout: snapshotBrowseTimeout,
	}
	handler.sendAndWait = func(agentID string, msg protocol.Message, timeout time.Duration) (<-chan protocol.Message, error) {
		return handler.Hub.SendAndWait(agentID, msg, timeout)
	}
	return handler
}

func RegisterSnapshotBrowseRoutes(rg *gin.RouterGroup, h *SnapshotBrowseHandler) {
	rg.POST("/agents/:id/snapshot-browse", h.BrowseSnapshot)
}

func (h *SnapshotBrowseHandler) BrowseSnapshot(c *gin.Context) {
	agentID := c.Param("id")
	if !agentExistsByID(c, h.DB, agentID) {
		return
	}
	if h.Hub == nil || !h.Hub.IsOnline(agentID) {
		writeErrorResponse(c, http.StatusBadGateway, "agent offline")
		return
	}
	supportsSnapshotBrowse, err := agentHasCapability(h.DB, agentID, protocol.CapabilitySnapshotBrowse)
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "database error")
		return
	}
	if !supportsSnapshotBrowse {
		writeErrorResponse(c, http.StatusBadRequest, "agent does not support snapshot browse")
		return
	}

	var request snapshotBrowseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, "invalid request")
		return
	}

	msg, err := protocol.NewMessage(protocol.TypeSnapshotBrowseReq, protocol.SnapshotBrowseReqPayload{
		SnapshotID: request.SnapshotID,
		Path:       request.Path,
	})
	if err != nil {
		writeErrorResponse(c, http.StatusInternalServerError, "encode snapshot browse request")
		return
	}

	wait := h.sendAndWait
	if wait == nil && h.Hub != nil {
		wait = h.Hub.SendAndWait
	}
	if wait == nil {
		writeErrorResponse(c, http.StatusBadGateway, "agent offline")
		return
	}
	respCh, err := wait(agentID, *msg, h.timeout)
	if err != nil {
		writeErrorResponse(c, http.StatusBadGateway, "agent offline")
		return
	}

	select {
	case resp, ok := <-respCh:
		if !ok {
			writeErrorResponse(c, http.StatusGatewayTimeout, "timeout waiting for agent response")
			return
		}
		payload, err := protocol.ParsePayload[protocol.SnapshotBrowseRespPayload](&resp)
		if err != nil {
			writeErrorResponse(c, http.StatusBadGateway, "invalid agent response")
			return
		}
		if payload.Error != "" {
			writeErrorResponse(c, http.StatusBadGateway, payload.Error)
			return
		}
		writeDataResponse(c, http.StatusOK, payload)
	case <-c.Request.Context().Done():
		writeErrorResponse(c, http.StatusGatewayTimeout, "request cancelled")
	}
}
