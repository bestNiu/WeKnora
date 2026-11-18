package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KBAccessStatsService owns per-member knowledge-base telemetry and the
// download risk guard.
type KBAccessStatsService interface {
	// RecordView counts one document preview by userID and appends an audit
	// entry. Best-effort: a telemetry failure never blocks the preview.
	RecordView(ctx context.Context, tenantID uint64, kbID, userID, documentID string) error
	// AuthorizeDownload applies the limits to one download request, charging
	// `units` documents (a batch download charges every file it contains, not
	// one), updates the counters, and reports a refusal or a risk freeze.
	AuthorizeDownload(ctx context.Context, tenantID uint64, kbID, userID string, units int) (types.KBDownloadDecision, error)
	// StatsForMembers returns the rows for the given members, keyed by user id.
	StatsForMembers(ctx context.Context, tenantID uint64, kbID string, userIDs []string) (map[string]*types.KBAccessStat, error)
	// ListRisk returns members with an active risk flag or freeze.
	ListRisk(ctx context.Context, tenantID uint64, kbID string) ([]*types.KBAccessStat, error)
	// ClearRisk lifts a member's risk flag and freeze (admin action).
	ClearRisk(ctx context.Context, tenantID uint64, kbID, userID, actorID string) error
}

// KBMemberTagService manages operator-assigned member labels on a KB.
type KBMemberTagService interface {
	// ListSummaries returns the distinct labels with member counts.
	ListSummaries(ctx context.Context, tenantID uint64, kbID string) ([]types.KBMemberTagSummary, error)
	// TagsByUser returns labels grouped by member, for the member panel.
	TagsByUser(ctx context.Context, tenantID uint64, kbID string, userIDs []string) (map[string][]string, error)
	// SetMemberTags replaces one member's labels; it is idempotent.
	SetMemberTags(ctx context.Context, tenantID uint64, kbID, userID string, tags []string) error
	// MembersWithTag returns the members carrying a label (unpaged, bounded by
	// the workspace size — used by the tag filter).
	MembersWithTag(ctx context.Context, tenantID uint64, kbID, tag string) ([]string, error)
}

// KBMemberTagRepository persists member labels.
type KBMemberTagRepository interface {
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.KBMemberTag, error)
	ListSummaries(ctx context.Context, tenantID uint64, kbID string) ([]types.KBMemberTagSummary, error)
	ReplaceForMember(ctx context.Context, tenantID uint64, kbID, userID string, tags []string) error
}

// KBAccessStatsRepository persists telemetry rows.
type KBAccessStatsRepository interface {
	Get(ctx context.Context, tenantID uint64, kbID, userID string) (*types.KBAccessStat, error)
	Create(ctx context.Context, stat *types.KBAccessStat) error
	Update(ctx context.Context, stat *types.KBAccessStat) error
	// ListByUsers returns the rows for a page of members.
	ListByUsers(ctx context.Context, tenantID uint64, kbID string, userIDs []string) ([]*types.KBAccessStat, error)
	// ListRisk returns rows with a risk flag or an active freeze.
	ListRisk(ctx context.Context, tenantID uint64, kbID string) ([]*types.KBAccessStat, error)
}
