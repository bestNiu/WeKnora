package handler

import (
	"net/http"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// AgentAccessHandler exposes the per-member visibility policy for one agent.
//
// Reading and writing the policy is an administrative action on a resource
// with a creator, so the routes use OwnedAgentOrAdmin: the agent creator, or a
// workspace Admin+. Every write is audited with the actor and the resulting
// member set.
type AgentAccessHandler struct {
	agentService  interfaces.CustomAgentService
	accessService interfaces.AgentAccessService
}

// NewAgentAccessHandler builds the handler.
func NewAgentAccessHandler(
	agentService interfaces.CustomAgentService,
	accessService interfaces.AgentAccessService,
) *AgentAccessHandler {
	return &AgentAccessHandler{agentService: agentService, accessService: accessService}
}

type agentAccessMemberPayload struct {
	UserID  string `json:"user_id"`
	CanView bool   `json:"can_view"`
}

type agentAccessPayload struct {
	VisibilityMode string                     `json:"visibility_mode"`
	Members        []agentAccessMemberPayload `json:"members"`
}

type agentAccessMemberResponse struct {
	UserID  string `json:"user_id"`
	CanView bool   `json:"can_view"`
}

type agentAccessResponse struct {
	AgentID        string                      `json:"agent_id"`
	VisibilityMode string                      `json:"visibility_mode"`
	Members        []agentAccessMemberResponse `json:"members"`
}

// GetAgentAccess returns the agent's member visibility policy.
//
// GET /agents/:id/access
func (h *AgentAccessHandler) GetAgentAccess(c *gin.Context) {
	ctx := c.Request.Context()
	agent, err := h.agentService.GetAgentByID(ctx, c.Param("id"))
	if err != nil || agent == nil {
		c.Error(apperrors.NewNotFoundError("agent not found"))
		return
	}
	rows, err := h.accessService.ListByAgent(ctx, agent.TenantID, agent.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewInternalServerError("failed to load agent access"))
		return
	}
	mode, err := h.accessService.Policy(ctx, agent.TenantID, agent.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewInternalServerError("failed to load agent access"))
		return
	}
	members := make([]agentAccessMemberResponse, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		members = append(members, agentAccessMemberResponse{UserID: row.UserID, CanView: row.CanView})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": agentAccessResponse{
		AgentID:        agent.ID,
		VisibilityMode: string(mode.Normalize()),
		Members:        members,
	}})
}

// PutAgentAccess replaces the agent's member visibility policy.
//
// PUT /agents/:id/access
func (h *AgentAccessHandler) PutAgentAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload agentAccessPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid access payload"))
		return
	}
	agent, err := h.agentService.GetAgentByID(ctx, c.Param("id"))
	if err != nil || agent == nil {
		c.Error(apperrors.NewNotFoundError("agent not found"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if agent.TenantID != caller.TenantID {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return
	}

	mode := types.AgentVisibilityMode(payload.VisibilityMode).Normalize()
	grants := make([]types.AgentUserGrant, 0, len(payload.Members))
	for _, member := range payload.Members {
		// The creator and Admins always see the agent; listing them
		// explicitly would imply their access is revocable, so drop them.
		if member.UserID == "" || member.UserID == agent.CreatedBy {
			continue
		}
		grants = append(grants, types.AgentUserGrant{UserID: member.UserID, CanView: member.CanView})
	}
	if err := h.accessService.SetPolicy(ctx, agent.TenantID, agent.ID, mode, caller.UserID); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	if err := h.accessService.ReplaceForAgent(ctx, agent.TenantID, agent.ID, caller.UserID, grants); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}

	if audit := middleware.AuditServiceFromContext(c); audit != nil {
		_ = audit.Log(ctx, &types.AuditLog{
			TenantID:      agent.TenantID,
			ActorUserID:   caller.UserID,
			ActorRole:     string(caller.Role),
			Action:        types.AuditActionAgentAccessUpdated,
			ScopeType:     "agent",
			ScopeID:       agent.ID,
			TargetType:    "agent",
			TargetID:      agent.ID,
			RequestPath:   c.Request.URL.Path,
			RequestMethod: c.Request.Method,
			Outcome:       types.AuditOutcomeSuccess,
			Details: types.JSON(mustJSON(map[string]any{
				"visibility_mode": string(mode),
				"members":         len(grants),
			})),
		})
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": agentAccessResponse{
		AgentID:        agent.ID,
		VisibilityMode: string(mode),
		Members:        toAgentAccessMemberResponses(grants),
	}})
}

// toAgentAccessMemberResponses converts grants for the API response.
func toAgentAccessMemberResponses(grants []types.AgentUserGrant) []agentAccessMemberResponse {
	members := make([]agentAccessMemberResponse, 0, len(grants))
	for _, grant := range grants {
		members = append(members, agentAccessMemberResponse{UserID: grant.UserID, CanView: grant.CanView})
	}
	return members
}
