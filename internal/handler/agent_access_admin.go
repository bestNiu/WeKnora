package handler

import (
	"context"
	"net/http"
	"strings"

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
//
// The dialog pages over workspace members instead of loading every member of
// the workspace, so both reads and writes are page-sized: GET returns one page
// of members with their identity, and PATCH applies only the rows the operator
// touched.
type AgentAccessHandler struct {
	agentService  interfaces.CustomAgentService
	accessService interfaces.AgentAccessService
	// memberService pages the workspace member list. Nil in reduced wirings,
	// which falls back to the small unit-test shape (grants only).
	memberService interfaces.TenantMemberService
	// userService hydrates username/email for the page. Nil costs the labels.
	userService interfaces.UserService
}

// NewAgentAccessHandler builds the handler.
func NewAgentAccessHandler(
	agentService interfaces.CustomAgentService,
	accessService interfaces.AgentAccessService,
	memberService interfaces.TenantMemberService,
	userService interfaces.UserService,
) *AgentAccessHandler {
	return &AgentAccessHandler{
		agentService:  agentService,
		accessService: accessService,
		memberService: memberService,
		userService:   userService,
	}
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
	UserID   string `json:"user_id"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
	Role     string `json:"role,omitempty"`
	// Status is the membership lifecycle state (active/invited/suspended), so
	// the panel can explain why a granted member cannot sign in right now.
	Status  string `json:"status,omitempty"`
	CanView bool   `json:"can_view"`
	// Implicit marks a member who sees the agent regardless of the allowlist
	// (creator / workspace Owner / Admin). Their row is a fact, not a choice.
	Implicit bool `json:"implicit"`
}

type agentAccessResponse struct {
	AgentID        string                      `json:"agent_id"`
	VisibilityMode string                      `json:"visibility_mode"`
	Members        []agentAccessMemberResponse `json:"members"`
	// Pagination over workspace members: the dialog must not render every
	// member of a large workspace at once.
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	// GrantedTotal is the number of live allowlist grants for this agent,
	// including members that are not on the current page.
	GrantedTotal int64 `json:"granted_total"`
}

// GetAgentAccess returns one page of the agent's member visibility policy.
//
// GET /agents/:id/access?page=&page_size=&q=
func (h *AgentAccessHandler) GetAgentAccess(c *gin.Context) {
	ctx := c.Request.Context()
	agent, err := h.agentService.GetAgentByID(ctx, c.Param("id"))
	if err != nil || agent == nil {
		c.Error(apperrors.NewNotFoundError("agent not found"))
		return
	}
	// An agent owned by another workspace is an explicit share, and the owning
	// workspace's allowlist deliberately does not apply to it (see
	// types.DecideAgentAccess). The policy is not the caller's to read either.
	caller := types.CallerFromContext(ctx)
	if agent.TenantID != caller.TenantID {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return
	}

	mode, err := h.accessService.Policy(ctx, agent.TenantID, agent.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewInternalServerError("failed to load agent access"))
		return
	}
	grants, err := h.accessService.ListByAgent(ctx, agent.TenantID, agent.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewInternalServerError("failed to load agent access"))
		return
	}
	granted := make(map[string]bool, len(grants))
	for _, row := range grants {
		if row != nil && row.CanView {
			granted[row.UserID] = true
		}
	}

	page, pageSize := parseAccessPage(c)
	query := strings.TrimSpace(c.Query("q"))
	members, total, err := h.listMembersPage(ctx, agent.TenantID, query, page, pageSize)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
		c.Error(apperrors.NewInternalServerError("failed to list members"))
		return
	}

	usersByID := map[string]*types.User{}
	if h.userService != nil && len(members) > 0 {
		ids := make([]string, 0, len(members))
		for _, member := range members {
			ids = append(ids, member.UserID)
		}
		if hydrated, hydrateErr := h.userService.GetUsersByIDs(ctx, ids); hydrateErr == nil {
			usersByID = hydrated
		} else {
			logger.Warnf(ctx, "Agent access panel: user lookup failed for %s: %v", agent.ID, hydrateErr)
		}
	}

	rows := make([]agentAccessMemberResponse, 0, len(members))
	for _, member := range members {
		if member == nil {
			continue
		}
		row := agentAccessMemberResponse{
			UserID:   member.UserID,
			Role:     string(member.Role),
			Status:   string(member.Status),
			CanView:  granted[member.UserID],
			Implicit: agentAccessImplicit(agent, member),
		}
		if user, ok := usersByID[member.UserID]; ok && user != nil {
			row.Username = user.Username
			row.Email = user.Email
		}
		rows = append(rows, row)
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": agentAccessResponse{
		AgentID:        agent.ID,
		VisibilityMode: string(mode.Normalize()),
		Members:        rows,
		Total:          total,
		Page:           page,
		PageSize:       pageSize,
		GrantedTotal:   int64(len(granted)),
	}})
}

// listMembersPage pages workspace members. A reduced wiring without a member
// service degrades to the pre-paging shape: the live grants, one page.
func (h *AgentAccessHandler) listMembersPage(
	ctx context.Context, tenantID uint64, query string, page, pageSize int,
) ([]*types.TenantMember, int64, error) {
	if h.memberService == nil {
		return nil, 0, nil
	}
	return h.memberService.ListMembersPage(ctx, tenantID, query, page, pageSize)
}

// agentAccessImplicit reports whether the member sees the agent regardless of
// the allowlist. Mirrors types.DecideAgentAccess so the UI never has to
// re-implement the rule.
func agentAccessImplicit(agent *types.CustomAgent, member *types.TenantMember) bool {
	if agent == nil || member == nil {
		return false
	}
	if agent.CreatedBy != "" && agent.CreatedBy == member.UserID {
		return true
	}
	return member.Role == types.TenantRoleOwner || member.Role == types.TenantRoleAdmin
}

// PutAgentAccess replaces the agent's member visibility policy.
//
// PUT /agents/:id/access
//
// Kept for API clients. The web panel uses PATCH, because a page-sized PUT
// would revoke every member the page did not contain.
func (h *AgentAccessHandler) PutAgentAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload agentAccessPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid access payload"))
		return
	}
	agent := h.agentForWrite(c)
	if agent == nil {
		return
	}
	caller := types.CallerFromContext(ctx)

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

	h.auditAccessWrite(c, agent, mode, "replace", len(grants))

	c.JSON(http.StatusOK, gin.H{"success": true, "data": agentAccessResponse{
		AgentID:        agent.ID,
		VisibilityMode: string(mode),
		Members:        toAgentAccessMemberResponses(grants),
		GrantedTotal:   int64(len(grants)),
	}})
}

// PatchAgentAccess applies incremental grant changes from the paged panel.
//
// PATCH /agents/:id/access
//
// Only the rows the operator touched are sent, so grants on other pages survive.
func (h *AgentAccessHandler) PatchAgentAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload agentAccessPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid access payload"))
		return
	}
	agent := h.agentForWrite(c)
	if agent == nil {
		return
	}
	caller := types.CallerFromContext(ctx)

	if raw := strings.TrimSpace(payload.VisibilityMode); raw != "" {
		mode := types.AgentVisibilityMode(raw)
		if !mode.IsValid() {
			c.Error(apperrors.NewBadRequestError("invalid visibility mode"))
			return
		}
		if err := h.accessService.SetPolicy(ctx, agent.TenantID, agent.ID, mode, caller.UserID); err != nil {
			logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
			c.Error(apperrors.NewInternalServerError("failed to update visibility mode"))
			return
		}
	}

	grants := make([]types.AgentUserGrant, 0, len(payload.Members))
	for _, member := range payload.Members {
		userID := strings.TrimSpace(member.UserID)
		// The creator always sees the agent; storing a row for them would
		// imply their access is revocable.
		if userID == "" || userID == agent.CreatedBy {
			continue
		}
		grants = append(grants, types.AgentUserGrant{UserID: userID, CanView: member.CanView})
	}
	if len(grants) > 0 {
		if err := h.accessService.UpsertForAgent(ctx, agent.TenantID, agent.ID, caller.UserID, grants); err != nil {
			logger.ErrorWithFields(ctx, err, map[string]any{"agent_id": agent.ID})
			c.Error(apperrors.NewInternalServerError("failed to update agent access"))
			return
		}
	}

	h.auditAccessWrite(c, agent,
		types.AgentVisibilityMode(payload.VisibilityMode).Normalize(), "patch", len(grants))

	c.JSON(http.StatusOK, gin.H{"success": true, "updated": len(grants)})
}

// agentForWrite resolves the agent for a policy write and rejects callers from
// another workspace. It writes the error response itself and returns nil when
// the request must stop.
func (h *AgentAccessHandler) agentForWrite(c *gin.Context) *types.CustomAgent {
	ctx := c.Request.Context()
	agent, err := h.agentService.GetAgentByID(ctx, c.Param("id"))
	if err != nil || agent == nil {
		c.Error(apperrors.NewNotFoundError("agent not found"))
		return nil
	}
	caller := types.CallerFromContext(ctx)
	if agent.TenantID != caller.TenantID {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return nil
	}
	return agent
}

// auditAccessWrite records the actor and the size of the resulting member set.
func (h *AgentAccessHandler) auditAccessWrite(
	c *gin.Context, agent *types.CustomAgent, mode types.AgentVisibilityMode,
	verb string, members int,
) {
	audit := middleware.AuditServiceFromContext(c)
	if audit == nil {
		return
	}
	ctx := c.Request.Context()
	caller := types.CallerFromContext(ctx)
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
			"mode":            verb,
			"members":         members,
		})),
	})
}

// toAgentAccessMemberResponses converts grants for the API response.
func toAgentAccessMemberResponses(grants []types.AgentUserGrant) []agentAccessMemberResponse {
	members := make([]agentAccessMemberResponse, 0, len(grants))
	for _, grant := range grants {
		members = append(members, agentAccessMemberResponse{UserID: grant.UserID, CanView: grant.CanView})
	}
	return members
}
