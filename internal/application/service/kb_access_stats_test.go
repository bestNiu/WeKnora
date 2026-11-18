package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbStatsRepo is an in-memory KBAccessStatsRepository for the guard tests.
type kbStatsRepo struct {
	rows map[string]*types.KBAccessStat
}

func newKBStatsRepo() *kbStatsRepo {
	return &kbStatsRepo{rows: map[string]*types.KBAccessStat{}}
}

func (r *kbStatsRepo) key(kbID, userID string) string { return kbID + "|" + userID }

func (r *kbStatsRepo) Get(_ context.Context, _ uint64, kbID, userID string) (*types.KBAccessStat, error) {
	return r.rows[r.key(kbID, userID)], nil
}

func (r *kbStatsRepo) Create(_ context.Context, stat *types.KBAccessStat) error {
	if stat.ID == "" {
		stat.ID = "stat-" + stat.UserID
	}
	copied := *stat
	r.rows[r.key(stat.KnowledgeBaseID, stat.UserID)] = &copied
	return nil
}

func (r *kbStatsRepo) Update(_ context.Context, stat *types.KBAccessStat) error {
	copied := *stat
	r.rows[r.key(stat.KnowledgeBaseID, stat.UserID)] = &copied
	return nil
}

func (r *kbStatsRepo) ListByUsers(
	_ context.Context, _ uint64, kbID string, userIDs []string,
) ([]*types.KBAccessStat, error) {
	var out []*types.KBAccessStat
	for _, id := range userIDs {
		if row, ok := r.rows[r.key(kbID, id)]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (r *kbStatsRepo) ListRisk(_ context.Context, _ uint64, kbID string) ([]*types.KBAccessStat, error) {
	var out []*types.KBAccessStat
	for _, row := range r.rows {
		if row.KnowledgeBaseID == kbID && (row.RiskFlag || row.DownloadSuspendedUntil != nil) {
			out = append(out, row)
		}
	}
	return out, nil
}

func newKBStatsService(t *testing.T, limits *config.KBDownloadLimitConfig) (*kbAccessStatsService, *kbStatsRepo) {
	t.Helper()
	repo := newKBStatsRepo()
	svc := NewKBAccessStatsService(repo, nil, &config.Config{KBDownload: limits}).(*kbAccessStatsService)
	return svc, repo
}

func dayLimit(n int) *config.KBDownloadLimitConfig {
	return &config.KBDownloadLimitConfig{DailyLimit: n}
}

// A preview is one view; repeated previews accumulate and stamp last_view_at.
func TestRecordViewCountsEveryPreview(t *testing.T) {
	svc, repo := newKBStatsService(t, dayLimit(20))
	ctx := context.Background()

	require.NoError(t, svc.RecordView(ctx, 7, "kb-1", "u1", "doc-1"))
	require.NoError(t, svc.RecordView(ctx, 7, "kb-1", "u1", "doc-2"))
	require.NoError(t, svc.RecordView(ctx, 7, "kb-1", "u2", "doc-1"))

	require.Equal(t, int64(2), repo.rows["kb-1|u1"].ViewsTotal)
	require.Equal(t, int64(1), repo.rows["kb-1|u2"].ViewsTotal)
	require.NotNil(t, repo.rows["kb-1|u1"].LastViewAt)
}

// The daily cap is a hard stop: the request after the limit is refused with the
// quota reason, and the counter does not grow further.
func TestAuthorizeDownloadEnforcesDailyQuota(t *testing.T) {
	svc, repo := newKBStatsService(t, dayLimit(2))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
		require.NoError(t, err)
		require.True(t, decision.Allowed)
	}

	decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.Equal(t, types.KBDownloadDeniedDailyQuota, decision.Reason)
	require.Equal(t, 2, decision.DownloadsToday)
	require.Equal(t, 2, decision.DailyLimit)
	require.Positive(t, decision.RetryAfter)
	require.Equal(t, int64(2), repo.rows["kb-1|u1"].DownloadsTotal, "a refused request must not count")
}

// A burst freezes downloads for the configured window instead of merely
// refusing one request, and the freeze is visible in the risk list.
func TestAuthorizeDownloadFreezesOnBurst(t *testing.T) {
	svc, repo := newKBStatsService(t, &config.KBDownloadLimitConfig{
		DailyLimit: 100, WindowMinutes: 10, WindowLimit: 2, SuspendHours: 24,
	})
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
		require.NoError(t, err)
		require.True(t, decision.Allowed)
	}

	decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.True(t, decision.RiskTriggered, "crossing the window threshold is an anomaly")
	require.Equal(t, types.KBDownloadDeniedSuspended, decision.Reason)
	require.NotNil(t, decision.SuspendedUntil)

	row := repo.rows["kb-1|u1"]
	require.True(t, row.RiskFlag)
	require.NotEmpty(t, row.RiskReason)
	require.NotNil(t, row.DownloadSuspendedUntil)

	// Once frozen, even a fresh request is refused without touching counters.
	before := row.DownloadsTotal
	decision, err = svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.Equal(t, types.KBDownloadDeniedSuspended, decision.Reason)
	require.Equal(t, before, repo.rows["kb-1|u1"].DownloadsTotal)

	risky, err := svc.ListRisk(ctx, 7, "kb-1")
	require.NoError(t, err)
	require.Len(t, risky, 1)
}

// Clearing the risk state gives the member their downloads back immediately.
func TestClearRiskLiftsFreeze(t *testing.T) {
	svc, repo := newKBStatsService(t, &config.KBDownloadLimitConfig{
		DailyLimit: 100, WindowMinutes: 10, WindowLimit: 1, SuspendHours: 24,
	})
	ctx := context.Background()

	_, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.False(t, decision.Allowed)

	require.NoError(t, svc.ClearRisk(ctx, 7, "kb-1", "u1", "admin-1"))
	row := repo.rows["kb-1|u1"]
	require.False(t, row.RiskFlag)
	require.Nil(t, row.DownloadSuspendedUntil)

	decision, err = svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.True(t, decision.Allowed, "after clearing, the guard allows again")
}

// Yesterday's counter must not leak into today.
func TestAuthorizeDownloadResetsQuotaOnNewDay(t *testing.T) {
	svc, repo := newKBStatsService(t, dayLimit(1))
	ctx := context.Background()

	_, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	row := repo.rows["kb-1|u1"]
	// Simulate a row written yesterday whose counter is already at the cap.
	yesterday := time.Now().AddDate(0, 0, -1)
	row.QuotaDay = &yesterday
	row.DownloadsQuotaDay = 1

	decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, 1, decision.DownloadsToday, "the new day starts from zero, then counts this request")
}

// Disabled limits (all zero) must never block: tests and deployments that do
// not opt into the guard behave exactly as before.
func TestAuthorizeDownloadInertWhenLimitsDisabled(t *testing.T) {
	svc, _ := newKBStatsService(t, &config.KBDownloadLimitConfig{})
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 1)
		require.NoError(t, err)
		require.True(t, decision.Allowed)
	}
}

var _ interfaces.KBAccessStatsRepository = (*kbStatsRepo)(nil)
// A batch download is charged per document: pulling 200 files must not cost the
// same as pulling one, or the daily cap would be trivially bypassed.
func TestAuthorizeDownloadChargesBatchUnits(t *testing.T) {
	svc, repo := newKBStatsService(t, dayLimit(20))
	ctx := context.Background()

	decision, err := svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 15)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, 15, decision.DownloadsToday)

	// 15 + 10 > 20: the whole batch is refused rather than partially served.
	decision, err = svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 10)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
	require.Equal(t, types.KBDownloadDeniedDailyQuota, decision.Reason)
	require.Equal(t, 15, repo.rows["kb-1|u1"].DownloadsQuotaDay, "a refused batch must not charge")

	// 15 + 5 == 20 fits exactly.
	decision, err = svc.AuthorizeDownload(ctx, 7, "kb-1", "u1", 5)
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, 20, repo.rows["kb-1|u1"].DownloadsQuotaDay)
}
