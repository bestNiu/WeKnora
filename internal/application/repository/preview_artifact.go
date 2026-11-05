package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// previewArtifactRepository stores derived PDF previews.
type previewArtifactRepository struct {
	db *gorm.DB
}

// NewPreviewArtifactRepository returns a repository backed by GORM.
func NewPreviewArtifactRepository(db *gorm.DB) interfaces.PreviewArtifactRepository {
	return &previewArtifactRepository{db: db}
}

// Get returns the artifact row, or nil when the knowledge item has none.
func (r *previewArtifactRepository) Get(
	ctx context.Context, tenantID uint64, knowledgeID string,
) (*types.KnowledgePreviewArtifact, error) {
	if knowledgeID == "" {
		return nil, errors.New("preview artifact: knowledge id is required")
	}
	var artifact types.KnowledgePreviewArtifact
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get preview artifact: %w", err)
	}
	return &artifact, nil
}

// Upsert inserts or replaces the row. The update list is explicit so a render
// finishing late cannot resurrect a deleted knowledge row's fields.
func (r *previewArtifactRepository) Upsert(
	ctx context.Context, artifact *types.KnowledgePreviewArtifact,
) error {
	if artifact == nil || artifact.KnowledgeID == "" || artifact.TenantID == 0 {
		return errors.New("preview artifact identity is required")
	}
	if artifact.Status == "" {
		artifact.Status = types.PreviewArtifactPending
	}
	now := time.Now()
	if artifact.CreatedAt.IsZero() {
		artifact.CreatedAt = now
	}
	artifact.UpdatedAt = now
	err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "knowledge_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"status":        artifact.Status,
			"storage_path":  artifact.StoragePath,
			"page_count":    artifact.PageCount,
			"content_hash":  artifact.ContentHash,
			"error_message": artifact.ErrorMessage,
			"updated_at":    now,
		}),
	}).Create(artifact).Error
	if err != nil {
		return fmt.Errorf("upsert preview artifact: %w", err)
	}
	return nil
}

// CountByStatus returns the artifact status distribution for one tenant.
func (r *previewArtifactRepository) CountByStatus(
	ctx context.Context, tenantID uint64,
) (map[string]int64, error) {
	type row struct {
		Status string
		Count  int64
	}
	var rows []row
	err := r.db.WithContext(ctx).Model(&types.KnowledgePreviewArtifact{}).
		Select("status, count(*) as count").
		Where("tenant_id = ?", tenantID).
		Group("status").Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("count preview artifacts: %w", err)
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Count
	}
	return out, nil
}

// Delete removes the row for one knowledge item.
func (r *previewArtifactRepository) Delete(
	ctx context.Context, tenantID uint64, knowledgeID string,
) error {
	if knowledgeID == "" {
		return errors.New("preview artifact: knowledge id is required")
	}
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Delete(&types.KnowledgePreviewArtifact{}).Error
	if err != nil {
		return fmt.Errorf("delete preview artifact: %w", err)
	}
	return nil
}
