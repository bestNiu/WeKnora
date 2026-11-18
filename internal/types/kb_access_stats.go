package types

import "time"

// KBAccessStat is one member's aggregated access activity on one knowledge
// base. It exists so the member list and the download guard never scan the
// audit stream: audit_logs stays the immutable detail trail, this row is the
// counter the hot path reads.
//
// Counters use lazy resets instead of a cron job: a writer compares the stored
// day/window with "now" and starts a new count when the period has rolled over.
type KBAccessStat struct {
	ID string `json:"id" gorm:"type:varchar(36);primaryKey"`
	// TenantID scopes every query; the list endpoint always filters on it.
	TenantID uint64 `json:"tenant_id" gorm:"index"`
	// KnowledgeBaseID + UserID identify the member. Unique together.
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_kb_access_stats_kb_user"`
	UserID          string `json:"user_id"          gorm:"type:varchar(36);not null;uniqueIndex:idx_kb_access_stats_kb_user"`

	ViewsTotal     int64 `json:"views_total"     gorm:"not null;default:0"`
	DownloadsTotal int64 `json:"downloads_total" gorm:"not null;default:0"`

	LastViewAt     *time.Time `json:"last_view_at,omitempty"`
	LastDownloadAt *time.Time `json:"last_download_at,omitempty"`

	// QuotaDay is the local date DownloadsQuotaDay belongs to.
	QuotaDay          *time.Time `json:"-"                   gorm:"type:date"`
	DownloadsQuotaDay int        `json:"downloads_today"      gorm:"not null;default:0"`

	// WindowStartedAt/DownloadsInWindow detect a burst; see KBDownloadLimits.
	WindowStartedAt   *time.Time `json:"-"`
	DownloadsInWindow int        `json:"downloads_in_window"  gorm:"not null;default:0"`

	RiskFlag               bool       `json:"risk_flag"                gorm:"not null;default:false"`
	RiskReason             string     `json:"risk_reason"              gorm:"type:varchar(200);not null;default:''"`
	DownloadSuspendedUntil *time.Time `json:"download_suspended_until,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TableName keeps the table name explicit so the migration and the model cannot
// drift apart through GORM pluralisation.
func (KBAccessStat) TableName() string { return "kb_access_stats" }

// DownloadSuspended reports whether the member's download is currently frozen.
func (s *KBAccessStat) DownloadSuspended(now time.Time) bool {
	return s != nil && s.DownloadSuspendedUntil != nil && s.DownloadSuspendedUntil.After(now)
}

// KBDownloadDenial enumerates why the download guard refused a request.
const (
	// KBDownloadDeniedDailyQuota: the member used up today's allowance.
	KBDownloadDeniedDailyQuota = "daily_quota"
	// KBDownloadDeniedSuspended: the member is in a risk freeze.
	KBDownloadDeniedSuspended = "suspended"
)

// KBDownloadLimits is the risk policy. Zero values disable a rule, which keeps
// the guard inert in tests and in deployments that never set it.
type KBDownloadLimits struct {
	// DailyLimit caps successful download requests per member per day.
	DailyLimit int
	// WindowMinutes + WindowLimit detect a burst: more than WindowLimit
	// requests inside WindowMinutes freezes downloads for SuspendFor.
	WindowMinutes int
	WindowLimit   int
	SuspendFor    time.Duration
}

// Enabled reports whether the guard should run at all.
func (l KBDownloadLimits) Enabled() bool {
	return l.DailyLimit > 0 || (l.WindowLimit > 0 && l.WindowMinutes > 0)
}

// KBDownloadDecision is the guard's verdict for one download request.
type KBDownloadDecision struct {
	Allowed bool
	// Reason is one of the KBDownloadDenied* constants when Allowed is false.
	Reason  string
	Message string
	// RetryAfter is when the refusal stops applying (quota rollover or freeze
	// expiry). Zero means "unknown".
	RetryAfter time.Duration
	// RiskTriggered reports that this request tripped the burst rule, so the
	// caller can surface an alert instead of a plain refusal.
	RiskTriggered  bool
	SuspendedUntil *time.Time
	DownloadsToday int
	DailyLimit     int
}
// KBMemberTag labels one member on one knowledge base. Labels are free-form and
// operator-assigned: they exist so a long member list can be filtered and
// granted in bulk.
type KBMemberTag struct {
	ID              string    `json:"id"                gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64    `json:"tenant_id"         gorm:"not null;index"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_kb_member_tags_unique"`
	UserID          string    `json:"user_id"           gorm:"type:varchar(36);not null;uniqueIndex:idx_kb_member_tags_unique"`
	Tag             string    `json:"tag"               gorm:"type:varchar(64);not null;uniqueIndex:idx_kb_member_tags_unique"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// TableName keeps the table name explicit for the same reason KBAccessStat does.
func (KBMemberTag) TableName() string { return "kb_member_tags" }

// KBMemberTagSummary is one distinct label with how many members carry it.
type KBMemberTagSummary struct {
	Tag   string `json:"tag"`
	Count int64  `json:"count"`
}

const (
	// KBMemberTagMaxPerMember bounds label noise per member.
	KBMemberTagMaxPerMember = 10
	// KBMemberTagMaxLength bounds a single label.
	KBMemberTagMaxLength = 32
)
