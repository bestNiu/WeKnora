package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// KBVisibilityMode controls who may read a knowledge base.
//
// The default is "tenant": every member of the KB's workspace can read it,
// which is the behaviour that predates per-user access. "restricted" narrows
// reads to kb_user_access rows with can_view, the KB creator, and the
// workspace Owner/Admin. Download is decided separately, by
// kb_user_access.can_download, so a readable KB is not necessarily
// downloadable.
type KBVisibilityMode string

const (
	// KBVisibilityTenant lets every workspace member read the KB.
	KBVisibilityTenant KBVisibilityMode = "tenant"
	// KBVisibilityRestricted limits reads to explicit can_view grants,
	// the KB creator and workspace Owner/Admin.
	KBVisibilityRestricted KBVisibilityMode = "restricted"
)

// IsValid reports whether the value is a defined visibility mode.
func (m KBVisibilityMode) IsValid() bool {
	return m == KBVisibilityTenant || m == KBVisibilityRestricted
}

// Normalize maps an empty or unknown mode to the tenant-wide default so a
// malformed row can never narrow access by accident.
func (m KBVisibilityMode) Normalize() KBVisibilityMode {
	if m.IsValid() {
		return m
	}
	return KBVisibilityTenant
}

// KBUserAccess is one per-(knowledge base, user) access override.
//
// A row is a positive grant: it can grant read access to a restricted KB and
// download access to any KB. It never revokes access the caller already has
// from ownership or role. Rows are soft-deleted so a re-grant keeps the audit
// trail; the partial unique index only covers live rows.
type KBUserAccess struct {
	ID              string `json:"id"                gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id"         gorm:"not null;index:idx_kb_user_access_tenant_user"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index:idx_kb_user_access_kb"`
	UserID          string `json:"user_id"           gorm:"type:varchar(36);not null;index:idx_kb_user_access_tenant_user"`
	// CanView admits the user to a restricted KB. It is a no-op for
	// tenant-visible KBs, which already admit every member.
	CanView bool `json:"can_view"     gorm:"not null;default:true"`
	// CanDownload grants the original-file download independently of the
	// tenant role. Contributors no longer download by role alone.
	CanDownload bool      `json:"can_download" gorm:"not null;default:false"`
	GrantedBy   string    `json:"granted_by"   gorm:"type:varchar(36)"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	// DeletedAt is the soft-delete marker. GORM filters deleted rows out of
	// normal queries; ReplaceForKB revives them in place so the audit trail and
	// any timestamps survive a revoke/re-grant cycle.
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index:idx_kb_user_access_deleted_at"`
}

// TableName returns the GORM table name.
func (KBUserAccess) TableName() string {
	return "kb_user_access"
}

// BeforeCreate assigns the UUID primary key.
func (a *KBUserAccess) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// KBUserGrant is one desired grant in a replace request.
type KBUserGrant struct {
	UserID      string `json:"user_id"`
	CanView     bool   `json:"can_view"`
	CanDownload bool   `json:"can_download"`
}

// KBAccessDecision is the resolved read/download capability for one caller.
type KBAccessDecision struct {
	CanView     bool
	CanDownload bool
}

// DecideKBAccess resolves a caller's capability for one knowledge base.
//
// callerUserID is the effective user: machine principals (API keys, IM and
// embed channels) carry their bound owner here. callerRole is the caller's
// role in the KB's workspace. systemAdmin bypasses every per-user rule, as it
// does elsewhere in the RBAC matrix.
//
// An empty callerUserID (unbound machine principal) never matches a grant, so
// a restricted KB stays invisible and nothing is downloadable — the fail-closed
// behaviour the access plan requires.
func DecideKBAccess(
	kb *KnowledgeBase,
	grant *KBUserAccess,
	callerUserID string,
	callerRole TenantRole,
	systemAdmin bool,
) KBAccessDecision {
	if kb == nil {
		return KBAccessDecision{}
	}
	if systemAdmin || callerRole.HasPermission(TenantRoleAdmin) {
		return KBAccessDecision{CanView: true, CanDownload: true}
	}
	if callerUserID != "" && kb.CreatorID == callerUserID {
		return KBAccessDecision{CanView: true, CanDownload: true}
	}

	view := kb.Visibility().Normalize() == KBVisibilityTenant
	download := false
	if grant != nil && callerUserID != "" {
		view = view || grant.CanView
		download = grant.CanDownload
	}
	return KBAccessDecision{CanView: view, CanDownload: view && download}
}
