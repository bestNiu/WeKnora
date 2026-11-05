package handler

import (
	"context"
	"encoding/json"
	"net/http"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// KBAccessAdminHandler exposes the per-user KB access policy for one knowledge
// base.
//
// Reading and writing the policy is an administrative action on a resource with
// a creator, so the routes use OwnedKBOrAdmin: the KB creator, or a workspace
// Admin+. Every write is audited with the actor and the resulting member set.
type KBAccessAdminHandler struct {
	kbService     interfaces.KnowledgeBaseService
	accessService interfaces.KBUserAccessService
	memberService interfaces.TenantMemberService
}

// NewKBAccessAdminHandler builds the handler.
func NewKBAccessAdminHandler(
	kbService interfaces.KnowledgeBaseService,
	accessService interfaces.KBUserAccessService,
	memberService interfaces.TenantMemberService,
) *KBAccessAdminHandler {
	return &KBAccessAdminHandler{
		kbService: kbService, accessService: accessService, memberService: memberService,
	}
}

type kbAccessMemberPayload struct {
	UserID      string `json:"user_id"`
	CanView     bool   `json:"can_view"`
	CanDownload bool   `json:"can_download"`
}

type kbAccessPayload struct {
	VisibilityMode string                  `json:"visibility_mode"`
	Members        []kbAccessMemberPayload `json:"members"`
}

type kbAccessMemberResponse struct {
	UserID      string `json:"user_id"`
	CanView     bool   `json:"can_view"`
	CanDownload bool   `json:"can_download"`
}

type kbAccessResponse struct {
	KnowledgeBaseID string                   `json:"knowledge_base_id"`
	VisibilityMode  string                   `json:"visibility_mode"`
	Members         []kbAccessMemberResponse `json:"members"`
	// CanDownload is the calling user's effective download permission, so the
	// UI can render the preview download button without guessing.
	CanDownload bool `json:"can_download"`
}

// GetKBAccess returns the KB's access policy.
//
// GET /knowledge-bases/:id/access
func (h *KBAccessAdminHandler) GetKBAccess(c *gin.Context) {
	ctx := c.Request.Context()
	kb, err := h.kbService.GetKnowledgeBaseByID(ctx, c.Param("id"))
	if err != nil || kb == nil {
		c.Error(apperrors.NewNotFoundError("knowledge base not found"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if kb.TenantID != caller.TenantID {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return
	}

	rows, err := h.accessService.ListByKB(ctx, kb.TenantID, kb.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to load knowledge base access"))
		return
	}
	members := make([]kbAccessMemberResponse, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		members = append(members, kbAccessMemberResponse{
			UserID: row.UserID, CanView: row.CanView, CanDownload: row.CanDownload,
		})
	}
	canDownload, _ := h.accessService.Decide(ctx, kb, caller.UserID, caller.Role,
		types.IsSystemAdminFromContext(ctx))
	c.JSON(http.StatusOK, gin.H{"success": true, "data": kbAccessResponse{
		KnowledgeBaseID: kb.ID,
		VisibilityMode:  string(kb.Visibility()),
		Members:         members,
		CanDownload:     canDownload.CanDownload,
	}})
}

// PutKBAccess replaces the KB's access policy.
//
// PUT /knowledge-bases/:id/access
func (h *KBAccessAdminHandler) PutKBAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload kbAccessPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid access payload"))
		return
	}
	kb, err := h.kbService.GetKnowledgeBaseByID(ctx, c.Param("id"))
	if err != nil || kb == nil {
		c.Error(apperrors.NewNotFoundError("knowledge base not found"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if kb.TenantID != caller.TenantID {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return
	}

	mode := types.KBVisibilityMode(payload.VisibilityMode).Normalize()
	grants := make([]types.KBUserGrant, 0, len(payload.Members))
	for _, member := range payload.Members {
		// The creator and Admins always see and download the KB; listing them
		// explicitly would imply their access is revocable, so drop them.
		if member.UserID == "" || member.UserID == kb.CreatorID {
			continue
		}
		grants = append(grants, types.KBUserGrant{
			UserID: member.UserID, CanView: member.CanView, CanDownload: member.CanDownload,
		})
	}
	if mode == types.KBVisibilityRestricted {
		// A restricted KB must stay usable by the people responsible for it.
		// Persist the creator and every active Owner/Admin as visible rows so
		// the policy table shows the full picture instead of relying on an
		// invisible implicit rule.
		merged := make(map[string]types.KBUserGrant, len(grants)+4)
		for _, grant := range grants {
			merged[grant.UserID] = grant
		}
		if kb.CreatorID != "" {
			merged[kb.CreatorID] = types.KBUserGrant{
				UserID: kb.CreatorID, CanView: true, CanDownload: true,
			}
		}
		if h.memberService != nil {
			if members, listErr := h.memberService.ListByTenant(ctx, kb.TenantID); listErr == nil {
				for _, member := range members {
					if member == nil || member.Status != types.TenantMemberStatusActive {
						continue
					}
					if member.Role != types.TenantRoleOwner && member.Role != types.TenantRoleAdmin {
						continue
					}
					grant := merged[member.UserID]
					grant.UserID = member.UserID
					grant.CanView = true
					merged[member.UserID] = grant
				}
			}
		}
		grants = grants[:0]
		for _, grant := range merged {
			grants = append(grants, grant)
		}
	}
	if err := h.accessService.ReplaceForKB(ctx, kb.TenantID, kb.ID, caller.UserID, grants); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewBadRequestError(err.Error()))
		return
	}
	setter, ok := h.kbService.(interface {
		SetVisibilityMode(context.Context, string, types.KBVisibilityMode) error
	})
	if !ok {
		c.Error(apperrors.NewInternalServerError("visibility mode is not supported"))
		return
	}
	if err := setter.SetVisibilityMode(ctx, kb.ID, mode); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to update visibility mode"))
		return
	}

	if audit := middleware.AuditServiceFromContext(c); audit != nil {
		_ = audit.Log(ctx, &types.AuditLog{
			TenantID:      kb.TenantID,
			ActorUserID:   caller.UserID,
			ActorRole:     string(caller.Role),
			Action:        types.AuditActionKBAccessUpdated,
			ScopeType:     "knowledge_base",
			ScopeID:       kb.ID,
			TargetType:    "knowledge_base",
			TargetID:      kb.ID,
			RequestPath:   c.Request.URL.Path,
			RequestMethod: c.Request.Method,
			Outcome:       types.AuditOutcomeSuccess,
			Details: types.JSON(mustJSON(map[string]any{
				"visibility_mode": string(mode),
				"members":         len(grants),
			})),
		})
	}

	c.JSON(http.StatusOK, gin.H{"success": true, "data": kbAccessResponse{
		KnowledgeBaseID: kb.ID,
		VisibilityMode:  string(mode),
		Members:         toKBAccessMemberResponses(grants),
	}})
}

// mustJSON marshals an audit detail map; audit must never fail a request
// because of a detail encoding problem.
func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// kbBatchAccessPayload grants one member set across several knowledge bases.
type kbBatchAccessPayload struct {
	KnowledgeBaseIDs []string                `json:"knowledge_base_ids"`
	Members          []kbAccessMemberPayload `json:"members"`
}

// PutKBBatchAccess grants access across several knowledge bases at once.
//
// POST /knowledge-bases/access/batch
//
// Bulk authorisation is Admin+ (the routes are not scoped to one KB, so the
// per-KB creator rule cannot apply). Existing grants are preserved: the request
// is a merge, so an admin can hand out download access without re-sending the
// whole policy of every KB.
func (h *KBAccessAdminHandler) PutKBBatchAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload kbBatchAccessPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid access payload"))
		return
	}
	if len(payload.KnowledgeBaseIDs) == 0 || len(payload.Members) == 0 {
		c.Error(apperrors.NewBadRequestError("knowledge_base_ids and members are required"))
		return
	}
	if len(payload.KnowledgeBaseIDs) > 200 || len(payload.Members) > 500 {
		c.Error(apperrors.NewBadRequestError("too many knowledge bases or members"))
		return
	}
	caller := types.CallerFromContext(ctx)
	applied := 0
	for _, kbID := range payload.KnowledgeBaseIDs {
		kb, err := h.kbService.GetKnowledgeBaseByID(ctx, kbID)
		if err != nil || kb == nil || kb.TenantID != caller.TenantID {
			continue
		}
		existing, err := h.accessService.ListByKB(ctx, kb.TenantID, kb.ID)
		if err != nil {
			c.Error(apperrors.NewInternalServerError("failed to load knowledge base access"))
			return
		}
		merged := make(map[string]types.KBUserGrant, len(existing)+len(payload.Members))
		for _, row := range existing {
			if row != nil {
				merged[row.UserID] = types.KBUserGrant{
					UserID: row.UserID, CanView: row.CanView, CanDownload: row.CanDownload,
				}
			}
		}
		for _, member := range payload.Members {
			if member.UserID == "" || member.UserID == kb.CreatorID {
				continue
			}
			merged[member.UserID] = types.KBUserGrant{
				UserID: member.UserID, CanView: member.CanView, CanDownload: member.CanDownload,
			}
		}
		grants := make([]types.KBUserGrant, 0, len(merged))
		for _, grant := range merged {
			grants = append(grants, grant)
		}
		if err := h.accessService.ReplaceForKB(ctx, kb.TenantID, kb.ID, caller.UserID, grants); err != nil {
			c.Error(apperrors.NewBadRequestError(err.Error()))
			return
		}
		applied++
	}
	if audit := middleware.AuditServiceFromContext(c); audit != nil {
		_ = audit.Log(ctx, &types.AuditLog{
			TenantID:      caller.TenantID,
			ActorUserID:   caller.UserID,
			ActorRole:     string(caller.Role),
			Action:        types.AuditActionKBAccessUpdated,
			ScopeType:     "knowledge_base",
			RequestPath:   c.Request.URL.Path,
			RequestMethod: c.Request.Method,
			Outcome:       types.AuditOutcomeSuccess,
			Details: types.JSON(mustJSON(map[string]any{
				"batch": true, "knowledge_bases": applied, "members": len(payload.Members),
			})),
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"updated": applied}})
}

func toKBAccessMemberResponses(grants []types.KBUserGrant) []kbAccessMemberResponse {
	members := make([]kbAccessMemberResponse, 0, len(grants))
	for _, grant := range grants {
		members = append(members, kbAccessMemberResponse{
			UserID: grant.UserID, CanView: grant.CanView, CanDownload: grant.CanDownload,
		})
	}
	return members
}
