package handler

import (
	"context"
	"net/http"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// PreviewAdminHandler exposes the derived-PDF preview backfill and its
// progress. Both endpoints are admin-only: one queues conversion work for the
// whole workspace, the other reports the aggregate job state.
type PreviewAdminHandler struct {
	kbService        interfaces.KnowledgeBaseService
	kgService        interfaces.KnowledgeService
	previewArtifacts interfaces.PreviewArtifactService
}

// NewPreviewAdminHandler builds the handler.
func NewPreviewAdminHandler(
	kbService interfaces.KnowledgeBaseService,
	kgService interfaces.KnowledgeService,
	previewArtifacts interfaces.PreviewArtifactService,
) *PreviewAdminHandler {
	return &PreviewAdminHandler{
		kbService: kbService, kgService: kgService, previewArtifacts: previewArtifacts,
	}
}

// Backfill queues a render for every renderable document in the workspace.
//
// POST /knowledge/preview/backfill
func (h *PreviewAdminHandler) Backfill(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID := types.MustTenantIDFromContext(ctx)
	backfiller, ok := h.previewArtifacts.(interface {
		Backfill(context.Context, []*types.KnowledgeBase, []*types.Knowledge) (int, error)
	})
	if !ok {
		c.Error(apperrors.NewInternalServerError("preview backfill is not supported"))
		return
	}
	kbs, err := h.kbService.ListKnowledgeBases(ctx)
	if err != nil {
		c.Error(apperrors.NewInternalServerError("failed to list knowledge bases"))
		return
	}
	knowledge := make([]*types.Knowledge, 0, len(kbs))
	for _, kb := range kbs {
		if kb == nil {
			continue
		}
		items, listErr := h.kgService.ListKnowledgeByKnowledgeBaseID(ctx, kb.ID)
		if listErr != nil {
			logger.Warnf(ctx, "Preview backfill: list %s failed: %v", kb.ID, listErr)
			continue
		}
		knowledge = append(knowledge, items...)
	}
	queued, err := backfiller.Backfill(ctx, kbs, knowledge)
	if err != nil {
		c.Error(apperrors.NewInternalServerError("preview backfill failed"))
		return
	}
	logger.Infof(ctx, "Preview backfill queued %d of %d documents (tenant %d)", queued, len(knowledge), tenantID)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"queued": queued, "documents": len(knowledge),
	}})
}

// BackfillStatus reports the artifact status distribution for the workspace.
//
// GET /knowledge/preview/backfill/status
func (h *PreviewAdminHandler) BackfillStatus(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID := types.MustTenantIDFromContext(ctx)
	stats, ok := h.previewArtifacts.(interface {
		Stats(context.Context, uint64) (map[string]int64, error)
	})
	if !ok {
		c.Error(apperrors.NewInternalServerError("preview status is not supported"))
		return
	}
	counts, err := stats.Stats(ctx, tenantID)
	if err != nil {
		c.Error(apperrors.NewInternalServerError("failed to read preview status"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": counts})
}
