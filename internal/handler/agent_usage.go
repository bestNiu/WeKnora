package handler

import (
	"net/http"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// maxAgentUsageIDs caps how many agents one usage request may ask about. The
// agent list is small, so this only bounds a hand-crafted request.
const maxAgentUsageIDs = 200

// AgentUsageHandler serves the per-agent usage counters shown on the agent list.
//
// The counters live on a separate endpoint instead of on GET /agents: that
// endpoint also feeds the chat input dropdown, which must stay a single cheap
// list query.
type AgentUsageHandler struct {
	agentService interfaces.CustomAgentService
	usageService interfaces.AgentUsageService
}

// NewAgentUsageHandler builds the handler.
func NewAgentUsageHandler(
	agentService interfaces.CustomAgentService,
	usageService interfaces.AgentUsageService,
) *AgentUsageHandler {
	return &AgentUsageHandler{agentService: agentService, usageService: usageService}
}

// ListAgentUsage returns usage counters for the requested agents.
//
// GET /agents/usage?ids=a,b,c
//
// Only agents of the caller's own workspace are counted: usage is platform-wide
// (see types.AgentUsage), so answering for an arbitrary ID would let a caller
// probe another workspace's traffic.
func (h *AgentUsageHandler) ListAgentUsage(c *gin.Context) {
	ctx := c.Request.Context()
	requested := parseAgentUsageIDs(c.Query("ids"))
	if len(requested) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"usage": []any{}}})
		return
	}
	if h.agentService == nil || h.usageService == nil {
		c.Error(apperrors.NewInternalServerError("agent usage is unavailable"))
		return
	}

	agents, err := h.agentService.ListAgents(ctx)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(apperrors.NewInternalServerError("failed to list agents"))
		return
	}
	allowed := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if agent != nil {
			allowed[agent.ID] = struct{}{}
		}
	}
	scoped := make([]string, 0, len(requested))
	for _, id := range requested {
		if _, ok := allowed[id]; ok {
			scoped = append(scoped, id)
		}
	}
	if len(scoped) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"usage": []any{}}})
		return
	}

	usage, err := h.usageService.UsageFor(ctx, scoped)
	if err != nil {
		logger.ErrorWithFields(ctx, err, nil)
		c.Error(apperrors.NewInternalServerError("failed to count agent usage"))
		return
	}
	rows := make([]types.AgentUsage, 0, len(scoped))
	for _, id := range scoped {
		if found, ok := usage[id]; ok && found != nil {
			rows = append(rows, *found)
			continue
		}
		rows = append(rows, types.AgentUsage{AgentID: id})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"usage": rows}})
}

// parseAgentUsageIDs splits the ids query parameter, dropping blanks,
// duplicates and anything past maxAgentUsageIDs.
func parseAgentUsageIDs(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0, 16)
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= maxAgentUsageIDs {
			break
		}
	}
	return ids
}
