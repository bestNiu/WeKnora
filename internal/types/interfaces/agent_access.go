package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// AgentAccessRepository persists per-(tenant, agent) visibility policy and the
// per-member allowlist.
//
// Every method is tenant-scoped: a row is only readable or replaceable through
// the tenant that owns the agent. Callers resolve the agent first (which
// already proves tenant visibility) and pass its TenantID.
type AgentAccessRepository interface {
	// ListPolicies returns the stored mode for the given agents, keyed by
	// agent ID. Agents without a row are absent (they are "tenant").
	ListPolicies(
		ctx context.Context, tenantID uint64, agentIDs []string,
	) (map[string]types.AgentVisibilityMode, error)
	// GetPolicy returns one agent's mode. A missing row is
	// AgentVisibilityTenant.
	GetPolicy(
		ctx context.Context, tenantID uint64, agentID string,
	) (types.AgentVisibilityMode, error)
	// SetPolicy upserts one agent's mode.
	SetPolicy(
		ctx context.Context, tenantID uint64, agentID string,
		mode types.AgentVisibilityMode, updatedBy string,
	) error
	// ListByAgent returns the live grants for one agent.
	ListByAgent(
		ctx context.Context, tenantID uint64, agentID string,
	) ([]*types.AgentUserAccess, error)
	// GetByUserAndAgents returns the caller's live grants for the given
	// agents, keyed by agent ID. Unknown IDs are simply absent.
	GetByUserAndAgents(
		ctx context.Context, tenantID uint64, userID string, agentIDs []string,
	) (map[string]*types.AgentUserAccess, error)
	// ReplaceForAgent makes the stored grant set match grants exactly:
	// missing users are soft-deleted, present users are created or revived.
	// The operation is transactional and idempotent.
	ReplaceForAgent(
		ctx context.Context, tenantID uint64, agentID, grantedBy string,
		grants []types.AgentUserGrant,
	) error
	// DeleteByAgent soft-deletes every grant for an agent (used when the
	// agent is deleted).
	DeleteByAgent(ctx context.Context, tenantID uint64, agentID string) error
}

// AgentAccessService is the application-layer view of agent member visibility.
//
// It owns the effective decision for one caller and agent so the HTTP list,
// get and chat handlers share a single rule instead of re-implementing it.
type AgentAccessService interface {
	// Policies returns the stored mode for the given agents. Agents without
	// a row are absent and default to "tenant".
	Policies(
		ctx context.Context, tenantID uint64, agentIDs []string,
	) (map[string]types.AgentVisibilityMode, error)
	// Policy returns one agent's mode, defaulting to "tenant".
	Policy(ctx context.Context, tenantID uint64, agentID string) (types.AgentVisibilityMode, error)
	// SetPolicy upserts one agent's mode.
	SetPolicy(
		ctx context.Context, tenantID uint64, agentID string,
		mode types.AgentVisibilityMode, updatedBy string,
	) error
	// ListByAgent lists one agent's grants for the admin UI.
	ListByAgent(
		ctx context.Context, tenantID uint64, agentID string,
	) ([]*types.AgentUserAccess, error)
	// ReplaceForAgent applies the desired grant set.
	ReplaceForAgent(
		ctx context.Context, tenantID uint64, agentID, grantedBy string,
		grants []types.AgentUserGrant,
	) error
	// DeleteByAgent drops every grant for an agent.
	DeleteByAgent(ctx context.Context, tenantID uint64, agentID string) error
	// Resolve returns the caller's live grants for the given agents.
	Resolve(
		ctx context.Context, tenantID uint64, userID string, agentIDs []string,
	) (map[string]*types.AgentUserAccess, error)
	// Decide resolves visibility for one agent and caller.
	Decide(
		ctx context.Context,
		agent *types.CustomAgent,
		caller types.Caller,
		systemAdmin bool,
	) (types.AgentAccessDecision, error)
	// DecideMany resolves visibility for several agents with one policy and
	// grant lookup per owning tenant.
	DecideMany(
		ctx context.Context,
		agents []*types.CustomAgent,
		caller types.Caller,
		systemAdmin bool,
	) (map[string]types.AgentAccessDecision, error)
}
