package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	userService   interfaces.UserService
	tagService    interfaces.KBMemberTagService
	// statsService carries usage counters and the download freeze state. Nil in
	// reduced wirings, which only costs the telemetry columns.
	statsService interfaces.KBAccessStatsService
}

// NewKBAccessAdminHandler builds the handler.
func NewKBAccessAdminHandler(
	kbService interfaces.KnowledgeBaseService,
	accessService interfaces.KBUserAccessService,
	memberService interfaces.TenantMemberService,
	userService interfaces.UserService,
	tagService interfaces.KBMemberTagService,
	statsService interfaces.KBAccessStatsService,
) *KBAccessAdminHandler {
	return &KBAccessAdminHandler{
		kbService: kbService, accessService: accessService, memberService: memberService,
		userService: userService, tagService: tagService, statsService: statsService,
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
	UserID      string          `json:"user_id"`
	Username    string          `json:"username,omitempty"`
	Email       string          `json:"email,omitempty"`
	Role        types.TenantRole `json:"role,omitempty"`
	CanView     bool            `json:"can_view"`
	CanDownload bool            `json:"can_download"`
	// Tags are operator labels used to filter and grant in bulk.
	Tags []string `json:"tags,omitempty"`
	// Telemetry: totals plus the state the risk guard maintains.
	ViewsTotal             int64      `json:"views_total"`
	DownloadsTotal         int64      `json:"downloads_total"`
	DownloadsToday         int        `json:"downloads_today"`
	LastViewAt             *time.Time `json:"last_view_at,omitempty"`
	LastDownloadAt         *time.Time `json:"last_download_at,omitempty"`
	RiskFlag               bool       `json:"risk_flag"`
	RiskReason             string     `json:"risk_reason,omitempty"`
	DownloadSuspendedUntil *time.Time `json:"download_suspended_until,omitempty"`
}

type kbAccessResponse struct {
	KnowledgeBaseID string                   `json:"knowledge_base_id"`
	VisibilityMode  string                   `json:"visibility_mode"`
	Members         []kbAccessMemberResponse `json:"members"`
	// Pagination over workspace members: the panel must not render every
	// member of a large workspace at once.
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
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

	page, pageSize := parseAccessPage(c)
	query := strings.TrimSpace(c.Query("q"))

	// Page over workspace members (not over grants): the panel has to show
	// members with no grant yet, so they can be granted.
	if h.memberService == nil {
		c.Error(apperrors.NewInternalServerError("member service unavailable"))
		return
	}
	tagFilter := strings.TrimSpace(c.Query("tag"))
	var (
		members []*types.TenantMember
		total   int64
	)
	if tagFilter != "" && h.tagService != nil {
		// Label filtering happens over the labelled set, then pages in memory:
		// a KB has hundreds of members at most, and joining labels into the
		// member repo would couple it to KB access.
		members, total, err = h.listMembersByTag(ctx, kb.TenantID, kb.ID, tagFilter, query, page, pageSize)
	} else {
		members, total, err = h.memberService.ListMembersPage(ctx, kb.TenantID, query, page, pageSize)
	}
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to list members"))
		return
	}

	grants, err := h.accessService.ListByKB(ctx, kb.TenantID, kb.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to load knowledge base access"))
		return
	}
	grantByUser := make(map[string]types.KBUserGrant, len(grants))
	for _, row := range grants {
		if row != nil {
			grantByUser[row.UserID] = types.KBUserGrant{
				UserID: row.UserID, CanView: row.CanView, CanDownload: row.CanDownload,
			}
		}
	}

	userIDs := make([]string, 0, len(members))
	for _, member := range members {
		userIDs = append(userIDs, member.UserID)
	}
	usersByID := map[string]*types.User{}
	if h.userService != nil {
		if hydrated, hydrateErr := h.userService.GetUsersByIDs(ctx, userIDs); hydrateErr == nil {
			usersByID = hydrated
		}
	}
	statsByUser := map[string]*types.KBAccessStat{}
	if h.statsService != nil {
		if stats, statsErr := h.statsService.StatsForMembers(ctx, kb.TenantID, kb.ID, userIDs); statsErr == nil {
			statsByUser = stats
		} else {
			logger.Warnf(ctx, "KB access panel: stats lookup failed for %s: %v", kb.ID, statsErr)
		}
	}

	tagsByUser := map[string][]string{}
	if h.tagService != nil && len(userIDs) > 0 {
		if tagged, tagErr := h.tagService.TagsByUser(ctx, kb.TenantID, kb.ID, userIDs); tagErr == nil {
			tagsByUser = tagged
		}
	}

	rows := make([]kbAccessMemberResponse, 0, len(members))
	for _, member := range members {
		row := kbAccessMemberResponse{UserID: member.UserID, Role: member.Role}
		row.Tags = tagsByUser[member.UserID]
		if user, ok := usersByID[member.UserID]; ok && user != nil {
			row.Username = user.Username
			row.Email = user.Email
		}
		if grant, ok := grantByUser[member.UserID]; ok {
			row.CanView = grant.CanView
			row.CanDownload = grant.CanDownload
		}
		if stat, ok := statsByUser[member.UserID]; ok && stat != nil {
			row.ViewsTotal = stat.ViewsTotal
			row.DownloadsTotal = stat.DownloadsTotal
			row.DownloadsToday = stat.DownloadsQuotaDay
			row.LastViewAt = stat.LastViewAt
			row.LastDownloadAt = stat.LastDownloadAt
			row.RiskFlag = stat.RiskFlag
			row.RiskReason = stat.RiskReason
			row.DownloadSuspendedUntil = stat.DownloadSuspendedUntil
		}
		rows = append(rows, row)
	}

	canDownload, _ := h.accessService.Decide(ctx, kb, caller.UserID, caller.Role,
		types.IsSystemAdminFromContext(ctx))
	c.JSON(http.StatusOK, gin.H{"success": true, "data": kbAccessResponse{
		KnowledgeBaseID: kb.ID,
		VisibilityMode:  string(kb.Visibility()),
		Members:         rows,
		Total:           total,
		Page:            page,
		PageSize:        pageSize,
		CanDownload:     canDownload.CanDownload,
	}})
}

// listMembersByTag filters workspace members by one label, then pages in
// memory. A KB has hundreds of members at most, so this stays cheap and keeps
// the member repository free of knowledge-base access concerns.
func (h *KBAccessAdminHandler) listMembersByTag(
	ctx context.Context, tenantID uint64, kbID, tag, query string, page, pageSize int,
) ([]*types.TenantMember, int64, error) {
	tagged, err := h.tagService.MembersWithTag(ctx, tenantID, kbID, tag)
	if err != nil {
		return nil, 0, err
	}
	if len(tagged) == 0 {
		return nil, 0, nil
	}
	all, err := h.memberService.ListByTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	allowed := make(map[string]struct{}, len(tagged))
	for _, id := range tagged {
		allowed[id] = struct{}{}
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	filtered := make([]*types.TenantMember, 0, len(tagged))
	for _, member := range all {
		if member == nil || member.Status != types.TenantMemberStatusActive {
			continue
		}
		if _, ok := allowed[member.UserID]; !ok {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(member.UserID), needle) {
			// Only the member ID is available here; name/email matching would
			// need the users table join. The panel's search box is therefore
			// most useful without a label filter, and label filtering is exact.
			continue
		}
		filtered = append(filtered, member)
	}
	total := int64(len(filtered))
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	return filtered[start:end], total, nil
}

// parseAccessPage reads the panel's pagination, clamped like every other list
// endpoint so a hostile page_size cannot ask for the whole workspace.
func parseAccessPage(c *gin.Context) (int, int) {
	const defaultPageSize, maxPageSize = 20, 100
	page, pageSize := 1, defaultPageSize
	if raw := c.Query("page"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if raw := c.Query("page_size"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

// PatchKBAccess applies incremental grant changes from a paged panel.
//
// PATCH /knowledge-bases/:id/access
//
// A page-sized PUT would revoke every member the page did not contain, so the
// panel sends only the rows the operator touched.
func (h *KBAccessAdminHandler) PatchKBAccess(c *gin.Context) {
	ctx := c.Request.Context()
	var payload struct {
		VisibilityMode string                  `json:"visibility_mode"`
		Members        []kbAccessMemberPayload `json:"members"`
	}
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

	if raw := strings.TrimSpace(payload.VisibilityMode); raw != "" {
		mode := types.KBVisibilityMode(raw).Normalize()
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
	}

	grants := make([]types.KBUserGrant, 0, len(payload.Members))
	for _, member := range payload.Members {
		userID := strings.TrimSpace(member.UserID)
		if userID == "" || userID == kb.CreatorID {
			// The creator always sees and downloads; storing a row for them only
			// confuses the UI.
			continue
		}
		grants = append(grants, types.KBUserGrant{
			UserID: userID, CanView: member.CanView, CanDownload: member.CanDownload,
		})
	}
	if len(grants) > 0 {
		if err := h.accessService.UpsertForKB(ctx, kb.TenantID, kb.ID, caller.UserID, grants); err != nil {
			logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
			c.Error(apperrors.NewInternalServerError("failed to update knowledge base access"))
			return
		}
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
				"mode": "patch", "members": len(grants),
			})),
		})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "updated": len(grants)})
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

// GetKBAccessTags lists the labels used on this knowledge base.
//
// GET /knowledge-bases/:id/access/tags
func (h *KBAccessAdminHandler) GetKBAccessTags(c *gin.Context) {
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
	if h.tagService == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []types.KBMemberTagSummary{}})
		return
	}
	rows, err := h.tagService.ListSummaries(ctx, kb.TenantID, kb.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to list tags"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": rows})
}

// PutKBAccessTags replaces one member's labels on this knowledge base.
//
// POST /knowledge-bases/:id/access/tags  {user_id, tags:[…]}
func (h *KBAccessAdminHandler) PutKBAccessTags(c *gin.Context) {
	ctx := c.Request.Context()
	var payload struct {
		UserID string   `json:"user_id"`
		Tags   []string `json:"tags"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.Error(apperrors.NewBadRequestError("invalid tag payload"))
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
	if strings.TrimSpace(payload.UserID) == "" {
		c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}
	if h.tagService == nil {
		c.Error(apperrors.NewInternalServerError("tag service unavailable"))
		return
	}
	if err := h.tagService.SetMemberTags(ctx, kb.TenantID, kb.ID, payload.UserID, payload.Tags); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to update tags"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// RecordKBView counts one document preview for the calling member.
//
// POST /knowledge-bases/:id/access/view  {document_id}
//
// Counting is a beacon, not a guarantee: telemetry failures are logged and
// swallowed so a preview never depends on the stats table.
func (h *KBAccessAdminHandler) RecordKBView(c *gin.Context) {
	ctx := c.Request.Context()
	kb, err := h.kbService.GetKnowledgeBaseByID(ctx, c.Param("id"))
	if err != nil || kb == nil {
		c.Error(apperrors.NewNotFoundError("knowledge base not found"))
		return
	}
	caller := types.CallerFromContext(ctx)
	if kb.TenantID != caller.TenantID || caller.UserID == "" {
		c.Error(apperrors.NewForbiddenError("permission denied"))
		return
	}

	var payload struct {
		DocumentID string `json:"document_id"`
	}
	_ = c.ShouldBindJSON(&payload)
	if h.statsService != nil {
		if err := h.statsService.RecordView(ctx, kb.TenantID, kb.ID, caller.UserID, payload.DocumentID); err != nil {
			logger.Warnf(ctx, "KB access: record view failed for %s: %v", kb.ID, err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

type kbAccessRiskMember struct {
	UserID                 string     `json:"user_id"`
	Username               string     `json:"username,omitempty"`
	Email                  string     `json:"email,omitempty"`
	ViewsTotal             int64      `json:"views_total"`
	DownloadsTotal         int64      `json:"downloads_total"`
	DownloadsToday         int        `json:"downloads_today"`
	LastDownloadAt         *time.Time `json:"last_download_at,omitempty"`
	RiskFlag               bool       `json:"risk_flag"`
	RiskReason             string     `json:"risk_reason,omitempty"`
	DownloadSuspendedUntil *time.Time `json:"download_suspended_until,omitempty"`
}

// ListKBAccessRisk returns the members flagged or frozen by the download guard.
//
// GET /knowledge-bases/:id/access/risk
func (h *KBAccessAdminHandler) ListKBAccessRisk(c *gin.Context) {
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
	if h.statsService == nil {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []kbAccessRiskMember{}})
		return
	}

	rows, err := h.statsService.ListRisk(ctx, kb.TenantID, kb.ID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID})
		c.Error(apperrors.NewInternalServerError("failed to list risk members"))
		return
	}
	userIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		userIDs = append(userIDs, row.UserID)
	}
	usersByID := map[string]*types.User{}
	if h.userService != nil && len(userIDs) > 0 {
		if hydrated, hydrateErr := h.userService.GetUsersByIDs(ctx, userIDs); hydrateErr == nil {
			usersByID = hydrated
		}
	}

	out := make([]kbAccessRiskMember, 0, len(rows))
	for _, row := range rows {
		item := kbAccessRiskMember{
			UserID: row.UserID, ViewsTotal: row.ViewsTotal, DownloadsTotal: row.DownloadsTotal,
			DownloadsToday: row.DownloadsQuotaDay, LastDownloadAt: row.LastDownloadAt,
			RiskFlag: row.RiskFlag, RiskReason: row.RiskReason,
			DownloadSuspendedUntil: row.DownloadSuspendedUntil,
		}
		if user, ok := usersByID[row.UserID]; ok && user != nil {
			item.Username = user.Username
			item.Email = user.Email
		}
		out = append(out, item)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

// ClearKBAccessRisk lifts a member's freeze and risk flag.
//
// DELETE /knowledge-bases/:id/access/risk/:user_id
func (h *KBAccessAdminHandler) ClearKBAccessRisk(c *gin.Context) {
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
	userID := strings.TrimSpace(c.Param("user_id"))
	if userID == "" {
		c.Error(apperrors.NewValidationError("user_id is required"))
		return
	}
	if h.statsService != nil {
		if err := h.statsService.ClearRisk(ctx, kb.TenantID, kb.ID, userID, caller.UserID); err != nil {
			logger.ErrorWithFields(ctx, err, map[string]interface{}{"knowledge_base_id": kb.ID, "user_id": userID})
			c.Error(apperrors.NewInternalServerError("failed to clear risk state"))
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}
