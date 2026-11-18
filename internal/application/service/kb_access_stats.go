package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbAccessStatsService owns per-member KB telemetry and the download guard.
type kbAccessStatsService struct {
	repo   interfaces.KBAccessStatsRepository
	audit  interfaces.AuditLogService
	limits types.KBDownloadLimits
}

// NewKBAccessStatsService builds the service. Limits come from config verbatim:
// config.LoadConfig materialises the defaults, and an explicit 0 there is how an
// operator turns one rule off.
func NewKBAccessStatsService(
	repo interfaces.KBAccessStatsRepository,
	audit interfaces.AuditLogService,
	cfg *config.Config,
) interfaces.KBAccessStatsService {
	limits := types.KBDownloadLimits{
		SuspendFor: time.Duration(config.DefaultKBDownloadSuspendHours) * time.Hour,
	}
	if cfg != nil && cfg.KBDownload != nil {
		limits.DailyLimit = cfg.KBDownload.DailyLimit
		limits.WindowMinutes = cfg.KBDownload.WindowMinutes
		limits.WindowLimit = cfg.KBDownload.WindowLimit
		if cfg.KBDownload.SuspendHours > 0 {
			limits.SuspendFor = time.Duration(cfg.KBDownload.SuspendHours) * time.Hour
		}
	}
	return &kbAccessStatsService{repo: repo, audit: audit, limits: limits}
}

// RecordView counts one preview. Telemetry must never fail a preview, so the
// caller only logs the returned error.
func (s *kbAccessStatsService) RecordView(
	ctx context.Context, tenantID uint64, kbID, userID, documentID string,
) error {
	if s == nil || s.repo == nil || tenantID == 0 || kbID == "" || userID == "" {
		return nil
	}
	now := time.Now()
	stat, err := s.repo.Get(ctx, tenantID, kbID, userID)
	if err != nil {
		return err
	}
	if stat == nil {
		stat = &types.KBAccessStat{
			TenantID: tenantID, KnowledgeBaseID: kbID, UserID: userID,
			ViewsTotal: 1, LastViewAt: &now,
		}
		if err := s.repo.Create(ctx, stat); err != nil {
			return err
		}
	} else {
		stat.ViewsTotal++
		stat.LastViewAt = &now
		if err := s.repo.Update(ctx, stat); err != nil {
			return err
		}
	}

	recordKBActivity(ctx, s.audit, tenantID, kbID, types.AuditActionKnowledgeViewed,
		"knowledge", documentID, types.AuditOutcomeSuccess, map[string]any{
			"user_id": userID,
		})
	return nil
}

// AuthorizeDownload applies the limits and updates the counters in one pass.
//
// The read-modify-write is not wrapped in a row lock: the deployment runs one
// application instance, and a lost increment under concurrency is worth less
// than the portability cost of SELECT ... FOR UPDATE (SQLite rejects it). The
// daily quota and the burst window are both advisory safety rails, not billing.
func (s *kbAccessStatsService) AuthorizeDownload(
	ctx context.Context, tenantID uint64, kbID, userID string, units int,
) (types.KBDownloadDecision, error) {
	if units < 1 {
		units = 1
	}
	decision := types.KBDownloadDecision{Allowed: true, DailyLimit: s.limits.DailyLimit}
	if s == nil || s.repo == nil || tenantID == 0 || kbID == "" || userID == "" {
		return decision, nil
	}

	now := time.Now()
	stat, err := s.repo.Get(ctx, tenantID, kbID, userID)
	if err != nil {
		return decision, err
	}
	isNew := stat == nil
	if isNew {
		stat = &types.KBAccessStat{TenantID: tenantID, KnowledgeBaseID: kbID, UserID: userID}
	}

	// 1) An existing freeze wins over everything else.
	if stat.DownloadSuspended(now) {
		decision.Allowed = false
		decision.Reason = types.KBDownloadDeniedSuspended
		decision.Message = "下载权限已被临时冻结，请联系管理员"
		decision.RetryAfter = stat.DownloadSuspendedUntil.Sub(now)
		decision.SuspendedUntil = stat.DownloadSuspendedUntil
		decision.DownloadsToday = stat.DownloadsQuotaDay
		s.auditDownload(ctx, tenantID, kbID, userID, types.AuditActionDownloadBlocked, map[string]any{
			"reason":                   types.KBDownloadDeniedSuspended,
			"suspended_until":          stat.DownloadSuspendedUntil,
			"risk_reason":              stat.RiskReason,
			"download_suspended_until": stat.DownloadSuspendedUntil,
		})
		return decision, nil
	}

	// 2) Lazy daily reset.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if stat.QuotaDay == nil || !sameDay(*stat.QuotaDay, today) {
		stat.QuotaDay = &today
		stat.DownloadsQuotaDay = 0
	}

	// A batch is charged per document: downloading 200 files must not cost the
	// same as downloading one.
	if s.limits.DailyLimit > 0 && stat.DownloadsQuotaDay+units > s.limits.DailyLimit {
		decision.Allowed = false
		decision.Reason = types.KBDownloadDeniedDailyQuota
		decision.Message = fmt.Sprintf("今日下载次数已达上限（%d 次），本次请求需要 %d 次额度，次日 00:00 重置",
			s.limits.DailyLimit, units)
		decision.RetryAfter = today.Add(24 * time.Hour).Sub(now)
		decision.DownloadsToday = stat.DownloadsQuotaDay
		s.auditDownload(ctx, tenantID, kbID, userID, types.AuditActionDownloadBlocked, map[string]any{
			"reason":          types.KBDownloadDeniedDailyQuota,
			"downloads_today": stat.DownloadsQuotaDay,
			"daily_limit":     s.limits.DailyLimit,
		})
		s.persist(ctx, stat, isNew)
		return decision, nil
	}

	// 3) Burst window. Crossing it freezes downloads for SuspendFor.
	windowReset := stat.WindowStartedAt == nil ||
		now.Sub(*stat.WindowStartedAt) > time.Duration(s.limits.WindowMinutes)*time.Minute
	if windowReset {
		stat.WindowStartedAt = &now
		stat.DownloadsInWindow = 0
	}
	stat.DownloadsInWindow += units

	if s.limits.WindowLimit > 0 && stat.DownloadsInWindow > s.limits.WindowLimit {
		until := now.Add(s.limits.SuspendFor)
		stat.RiskFlag = true
		stat.RiskReason = fmt.Sprintf("%d 分钟内下载 %d 次，超过阈值 %d",
			s.limits.WindowMinutes, stat.DownloadsInWindow, s.limits.WindowLimit)
		stat.DownloadSuspendedUntil = &until
		decision.Allowed = false
		decision.Reason = types.KBDownloadDeniedSuspended
		decision.Message = "下载频率异常，已临时冻结下载权限，请联系管理员"
		decision.RiskTriggered = true
		decision.SuspendedUntil = &until
		decision.RetryAfter = s.limits.SuspendFor
		decision.DownloadsToday = stat.DownloadsQuotaDay
		s.persist(ctx, stat, isNew)
		s.auditDownload(ctx, tenantID, kbID, userID, types.AuditActionDownloadSuspended, map[string]any{
			"reason":              stat.RiskReason,
			"downloads_in_window": stat.DownloadsInWindow,
			"window_minutes":      s.limits.WindowMinutes,
			"suspended_until":     until,
		})
		return decision, nil
	}

	// 4) Allowed: count it.
	stat.DownloadsQuotaDay += units
	stat.DownloadsTotal += int64(units)
	stat.LastDownloadAt = &now
	decision.DownloadsToday = stat.DownloadsQuotaDay
	s.persist(ctx, stat, isNew)
	s.auditDownload(ctx, tenantID, kbID, userID, types.AuditActionKnowledgeDownloaded, map[string]any{
		"downloads_today": stat.DownloadsQuotaDay,
		"daily_limit":     s.limits.DailyLimit,
	})
	return decision, nil
}

// StatsForMembers returns the rows for one page of members, keyed by user id.
func (s *kbAccessStatsService) StatsForMembers(
	ctx context.Context, tenantID uint64, kbID string, userIDs []string,
) (map[string]*types.KBAccessStat, error) {
	rows, err := s.repo.ListByUsers(ctx, tenantID, kbID, userIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*types.KBAccessStat, len(rows))
	for _, row := range rows {
		if row != nil {
			out[row.UserID] = row
		}
	}
	return out, nil
}

// ListRisk returns members flagged or frozen on this knowledge base.
func (s *kbAccessStatsService) ListRisk(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KBAccessStat, error) {
	return s.repo.ListRisk(ctx, tenantID, kbID)
}

// ClearRisk lifts a member's freeze and risk flag.
func (s *kbAccessStatsService) ClearRisk(
	ctx context.Context, tenantID uint64, kbID, userID, actorID string,
) error {
	stat, err := s.repo.Get(ctx, tenantID, kbID, userID)
	if err != nil {
		return err
	}
	if stat == nil {
		return nil
	}
	stat.RiskFlag = false
	stat.RiskReason = ""
	stat.DownloadSuspendedUntil = nil
	stat.DownloadsInWindow = 0
	stat.WindowStartedAt = nil
	if err := s.repo.Update(ctx, stat); err != nil {
		return err
	}
	recordKBActivity(ctx, s.audit, tenantID, kbID, types.AuditActionKBAccessUpdated,
		"user", userID, types.AuditOutcomeSuccess, map[string]any{
			"action": "risk_cleared", "actor_user_id": actorID,
		})
	return nil
}

// persist writes counters; create-on-first-sight is handled by the repo so the
// insert and update paths stay distinguishable in logs.
func (s *kbAccessStatsService) persist(ctx context.Context, stat *types.KBAccessStat, create bool) {
	var err error
	if create || stat.ID == "" {
		err = s.repo.Create(ctx, stat)
	} else {
		err = s.repo.Update(ctx, stat)
	}
	if err != nil {
		logger.Warnf(ctx, "kb access stats: persist failed for %s/%s: %v", stat.KnowledgeBaseID, stat.UserID, err)
	}
}

// auditDownload records one download decision. Best-effort, like every other
// activity write: a telemetry outage must not fail a user's download.
func (s *kbAccessStatsService) auditDownload(
	ctx context.Context, tenantID uint64, kbID, userID string, action types.AuditAction, details map[string]any,
) {
	recordKBActivity(ctx, s.audit, tenantID, kbID, action, "user", userID,
		types.AuditOutcomeSuccess, details)
}

func sameDay(a, b time.Time) bool {
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
