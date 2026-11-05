package types

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AgentVisibilityMode controls who may see and use an agent inside the
// workspace that owns it.
//
// The default is "tenant": every member of the workspace sees it, which is the
// behaviour that predates per-member access. "allowlist" narrows the agent to
// agent_user_access rows with can_view, plus the agent creator and the
// workspace Owner/Admin.
//
// The mode is stored in agent_access_policy rather than on custom_agents so a
// built-in agent (which may have no custom_agents row until it is customised)
// can be restricted too, and so a policy write never touches agent config.
type AgentVisibilityMode string

const (
	// AgentVisibilityTenant lets every workspace member see the agent.
	AgentVisibilityTenant AgentVisibilityMode = "tenant"
	// AgentVisibilityAllowlist limits the agent to explicit can_view grants,
	// its creator and workspace Owner/Admin.
	AgentVisibilityAllowlist AgentVisibilityMode = "allowlist"
)

// IsValid reports whether the value is a defined visibility mode.
func (m AgentVisibilityMode) IsValid() bool {
	return m == AgentVisibilityTenant || m == AgentVisibilityAllowlist
}

// Normalize maps an empty or unknown mode to the tenant-wide default so a
// malformed row can never narrow access by accident.
func (m AgentVisibilityMode) Normalize() AgentVisibilityMode {
	if m.IsValid() {
		return m
	}
	return AgentVisibilityTenant
}

// AgentAccessPolicy is the per-(tenant, agent) visibility mode row.
//
// A missing row means "tenant": reading a policy never has to create one, and
// only an explicit restriction (or its removal) writes here.
type AgentAccessPolicy struct {
	TenantID       uint64    `json:"tenant_id"       gorm:"primaryKey"`
	AgentID        string    `json:"agent_id"        gorm:"type:varchar(36);primaryKey"`
	VisibilityMode string    `json:"visibility_mode" gorm:"type:varchar(16);not null;default:'tenant'"`
	UpdatedBy      string    `json:"updated_by"      gorm:"type:varchar(36)"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// TableName returns the GORM table name.
func (AgentAccessPolicy) TableName() string {
	return "agent_access_policy"
}

// AgentUserAccess is one per-(agent, user) access override.
//
// A row is a positive grant: can_view admits the user to an allowlist agent.
// It never revokes access the caller already has from ownership or role. Rows
// are soft-deleted so a revoke keeps the audit trail; the partial unique index
// only covers live rows.
type AgentUserAccess struct {
	ID        string    `json:"id"         gorm:"type:varchar(36);primaryKey"`
	TenantID  uint64    `json:"tenant_id"  gorm:"not null;index:idx_agent_user_access_tenant_user"`
	AgentID   string    `json:"agent_id"   gorm:"type:varchar(36);not null;index:idx_agent_user_access_agent"`
	UserID    string    `json:"user_id"    gorm:"type:varchar(36);not null;index:idx_agent_user_access_tenant_user"`
	CanView   bool      `json:"can_view"   gorm:"not null;default:true"`
	GrantedBy string    `json:"granted_by" gorm:"type:varchar(36)"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt is the soft-delete marker. GORM filters deleted rows out of
	// normal queries; ReplaceForAgent revives them in place so the audit trail
	// and any timestamps survive a revoke/re-grant cycle.
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index:idx_agent_user_access_deleted_at"`
}

// TableName returns the GORM table name.
func (AgentUserAccess) TableName() string {
	return "agent_user_access"
}

// BeforeCreate assigns the UUID primary key.
func (a *AgentUserAccess) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	return nil
}

// AgentUserGrant is the writable form of one allowlist entry.
type AgentUserGrant struct {
	UserID  string
	CanView bool
}

// AgentAccessDecision is the effective visibility of one agent for one caller.
type AgentAccessDecision struct {
	Visible bool
}

// DecideAgentAccess resolves whether callerUserID may see agent.
//
// Rules, in order:
//  1. a system admin sees every agent;
//  2. the workspace Owner/Admin sees every agent;
//  3. the agent creator sees their own agent;
//  4. an agent owned by another tenant is an explicit share, so the owning
//     tenant's member allowlist does not apply to the caller;
//  5. a "tenant" agent is visible to every member;
//  6. an "allowlist" agent is visible only with a live can_view grant.
func DecideAgentAccess(
	agent *CustomAgent,
	mode AgentVisibilityMode,
	grant *AgentUserAccess,
	callerTenantID uint64,
	callerUserID string,
	callerRole TenantRole,
	systemAdmin bool,
) AgentAccessDecision {
	if systemAdmin {
		return AgentAccessDecision{Visible: true}
	}
	if callerRole == TenantRoleOwner || callerRole == TenantRoleAdmin {
		return AgentAccessDecision{Visible: true}
	}
	if agent != nil && agent.CreatedBy != "" && agent.CreatedBy == callerUserID {
		return AgentAccessDecision{Visible: true}
	}
	if agent != nil && callerTenantID != 0 && agent.TenantID != callerTenantID {
		return AgentAccessDecision{Visible: true}
	}
	if mode.Normalize() == AgentVisibilityTenant {
		return AgentAccessDecision{Visible: true}
	}
	return AgentAccessDecision{Visible: grant != nil && grant.CanView}
}
