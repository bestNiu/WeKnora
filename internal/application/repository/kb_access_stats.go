package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbAccessStatsRepository stores per-member KB telemetry.
type kbAccessStatsRepository struct {
	db *gorm.DB
}

// NewKBAccessStatsRepository builds the repository.
func NewKBAccessStatsRepository(db *gorm.DB) interfaces.KBAccessStatsRepository {
	return &kbAccessStatsRepository{db: db}
}

// Get returns the member's row, or (nil, nil) when the member has never been
// seen. "No row" is the normal first-visit state, not an error.
func (r *kbAccessStatsRepository) Get(
	ctx context.Context, tenantID uint64, kbID, userID string,
) (*types.KBAccessStat, error) {
	if tenantID == 0 || kbID == "" || userID == "" {
		return nil, nil
	}
	var stat types.KBAccessStat
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND user_id = ?", tenantID, kbID, userID).
		First(&stat).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &stat, nil
}

// Create inserts a new telemetry row.
func (r *kbAccessStatsRepository) Create(ctx context.Context, stat *types.KBAccessStat) error {
	if stat == nil {
		return errors.New("stat is required")
	}
	if stat.ID == "" {
		stat.ID = uuid.NewString()
	}
	now := time.Now()
	if stat.CreatedAt.IsZero() {
		stat.CreatedAt = now
	}
	stat.UpdatedAt = now
	return r.db.WithContext(ctx).Create(stat).Error
}

// Update writes the mutable counters and risk state.
func (r *kbAccessStatsRepository) Update(ctx context.Context, stat *types.KBAccessStat) error {
	if stat == nil || stat.ID == "" {
		return errors.New("stat id is required")
	}
	stat.UpdatedAt = time.Now()
	return r.db.WithContext(ctx).
		Model(&types.KBAccessStat{}).
		Where("id = ?", stat.ID).
		Updates(map[string]interface{}{
			"views_total":              stat.ViewsTotal,
			"downloads_total":          stat.DownloadsTotal,
			"last_view_at":             stat.LastViewAt,
			"last_download_at":         stat.LastDownloadAt,
			"quota_day":                stat.QuotaDay,
			"downloads_quota_day":      stat.DownloadsQuotaDay,
			"window_started_at":        stat.WindowStartedAt,
			"downloads_in_window":      stat.DownloadsInWindow,
			"risk_flag":                stat.RiskFlag,
			"risk_reason":              stat.RiskReason,
			"download_suspended_until": stat.DownloadSuspendedUntil,
			"updated_at":               stat.UpdatedAt,
		}).Error
}

// ListByUsers returns the rows for one page of members. An empty user list is a
// no-op rather than a query matching everything.
func (r *kbAccessStatsRepository) ListByUsers(
	ctx context.Context, tenantID uint64, kbID string, userIDs []string,
) ([]*types.KBAccessStat, error) {
	if tenantID == 0 || kbID == "" || len(userIDs) == 0 {
		return nil, nil
	}
	var stats []*types.KBAccessStat
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND user_id IN ?", tenantID, kbID, userIDs).
		Find(&stats).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// ListRisk returns members currently flagged or frozen, newest activity first.
func (r *kbAccessStatsRepository) ListRisk(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KBAccessStat, error) {
	if tenantID == 0 || kbID == "" {
		return nil, nil
	}
	var stats []*types.KBAccessStat
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND (risk_flag = ? OR download_suspended_until > ?)",
			tenantID, kbID, true, time.Now()).
		Order("updated_at DESC").
		Limit(500).
		Find(&stats).Error
	if err != nil {
		return nil, err
	}
	return stats, nil
}