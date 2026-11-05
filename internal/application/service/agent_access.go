package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agentAccessService resolves agent member visibility and owns the admin-side
// allowlist management.
//
// Resolution is request-local: every call reads the current rows, so a revoked
// grant takes effect on the next request.
type agentAccessService struct {
	repo interfaces.AgentAccessRepository
}

// NewAgentAccessService returns an access service backed by repo.
func NewAgentAccessService(repo interfaces.AgentAccessRepository) interfaces.AgentAccessService {
	return &agentAccessService{repo: repo}
}

// Policies returns the stored modes for the given agents.
func (s *agentAccessService) Policies(
	ctx context.Context, tenantID uint64, agentIDs []string,
) (map[string]types.AgentVisibilityMode, error) {
	if s == nil || s.repo == nil {
		return map[string]types.AgentVisibilityMode{}, nil
	}
	return s.repo.ListPolicies(ctx, tenantID, agentIDs)
}

// Policy returns one agent's mode, defaulting to tenant.
func (s *agentAccessService) Policy(
	ctx context.Context, tenantID uint64, agentID string,
) (types.AgentVisibilityMode, error) {
	if s == nil || s.repo == nil {
		return types.AgentVisibilityTenant, nil
	}
	return s.repo.GetPolicy(ctx, tenantID, agentID)
}

// SetPolicy upserts one agent's mode.
func (s *agentAccessService) SetPolicy(
	ctx context.Context, tenantID uint64, agentID string,
	mode types.AgentVisibilityMode, updatedBy string,
) error {
	if s == nil || s.repo == nil {
		return errors.New("agent access service is unavailable")
	}
	if tenantID == 0 {
		return errors.New("agent access: tenant id is required")
	}
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent access: agent id is required")
	}
	if !mode.IsValid() {
		return fmt.Errorf("agent access: invalid visibility mode %q", mode)
	}
	return s.repo.SetPolicy(ctx, tenantID, agentID, mode, updatedBy)
}

// ListByAgent lists one agent's grants for the admin UI.
func (s *agentAccessService) ListByAgent(
	ctx context.Context, tenantID uint64, agentID string,
) ([]*types.AgentUserAccess, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("agent access service is unavailable")
	}
	return s.repo.ListByAgent(ctx, tenantID, agentID)
}

// ReplaceForAgent applies the desired grant set.
func (s *agentAccessService) ReplaceForAgent(
	ctx context.Context, tenantID uint64, agentID, grantedBy string,
	grants []types.AgentUserGrant,
) error {
	if s == nil || s.repo == nil {
		return errors.New("agent access service is unavailable")
	}
	if tenantID == 0 {
		return errors.New("agent access: tenant id is required")
	}
	if strings.TrimSpace(agentID) == "" {
		return errors.New("agent access: agent id is required")
	}
	return s.repo.ReplaceForAgent(ctx, tenantID, agentID, grantedBy, grants)
}

// DeleteByAgent drops every grant for an agent.
func (s *agentAccessService) DeleteByAgent(
	ctx context.Context, tenantID uint64, agentID string,
) error {
	if s == nil || s.repo == nil {
		return errors.New("agent access service is unavailable")
	}
	return s.repo.DeleteByAgent(ctx, tenantID, agentID)
}

// Resolve returns the caller's live grants for the given agents.
func (s *agentAccessService) Resolve(
	ctx context.Context, tenantID uint64, userID string, agentIDs []string,
) (map[string]*types.AgentUserAccess, error) {
	if s == nil || s.repo == nil {
		return map[string]*types.AgentUserAccess{}, nil
	}
	return s.repo.GetByUserAndAgents(ctx, tenantID, userID, agentIDs)
}

// Decide resolves visibility for one agent and caller.
func (s *agentAccessService) Decide(
	ctx context.Context,
	agent *types.CustomAgent,
	caller types.Caller,
	systemAdmin bool,
) (types.AgentAccessDecision, error) {
	if agent == nil {
		return types.AgentAccessDecision{}, nil
	}
	// Fast path: these callers see every agent regardless of policy.
	if systemAdmin || caller.Role == types.TenantRoleOwner || caller.Role == types.TenantRoleAdmin ||
		(agent.CreatedBy != "" && agent.CreatedBy == caller.UserID) ||
		(caller.TenantID != 0 && agent.TenantID != caller.TenantID) {
		return types.DecideAgentAccess(agent, types.AgentVisibilityTenant, nil,
			caller.TenantID, caller.UserID, caller.Role, systemAdmin), nil
	}
	if s == nil || s.repo == nil {
		return types.DecideAgentAccess(agent, types.AgentVisibilityTenant, nil,
			caller.TenantID, caller.UserID, caller.Role, systemAdmin), nil
	}
	mode, err := s.repo.GetPolicy(ctx, agent.TenantID, agent.ID)
	if err != nil {
		return types.AgentAccessDecision{}, fmt.Errorf("resolve agent access: %w", err)
	}
	if mode.Normalize() == types.AgentVisibilityTenant {
		return types.AgentAccessDecision{Visible: true}, nil
	}
	grants, err := s.repo.GetByUserAndAgents(ctx, agent.TenantID, caller.UserID, []string{agent.ID})
	if err != nil {
		return types.AgentAccessDecision{}, fmt.Errorf("resolve agent access: %w", err)
	}
	return types.DecideAgentAccess(agent, mode, grants[agent.ID],
		caller.TenantID, caller.UserID, caller.Role, systemAdmin), nil
}

// DecideMany resolves visibility for several agents with one policy and grant
// lookup per owning tenant.
func (s *agentAccessService) DecideMany(
	ctx context.Context,
	agents []*types.CustomAgent,
	caller types.Caller,
	systemAdmin bool,
) (map[string]types.AgentAccessDecision, error) {
	decisions := make(map[string]types.AgentAccessDecision, len(agents))
	if len(agents) == 0 {
		return decisions, nil
	}
	if systemAdmin || caller.Role == types.TenantRoleOwner || caller.Role == types.TenantRoleAdmin ||
		s == nil || s.repo == nil {
		for _, agent := range agents {
			if agent != nil {
				decisions[agent.ID] = types.AgentAccessDecision{Visible: true}
			}
		}
		return decisions, nil
	}

	// Agents are tenant-scoped, but a shared agent belongs to a different
	// tenant than the caller's. Group the lookup by owning tenant so one query
	// per tenant resolves the whole page.
	idsByTenant := make(map[uint64][]string)
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		idsByTenant[agent.TenantID] = append(idsByTenant[agent.TenantID], agent.ID)
	}
	modesByAgent := make(map[string]types.AgentVisibilityMode, len(agents))
	grantsByAgent := make(map[string]*types.AgentUserAccess, len(agents))
	for tenantID, ids := range idsByTenant {
		modes, err := s.repo.ListPolicies(ctx, tenantID, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve agent access: %w", err)
		}
		for agentID, mode := range modes {
			modesByAgent[agentID] = mode
		}
		grants, err := s.repo.GetByUserAndAgents(ctx, tenantID, caller.UserID, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve agent access: %w", err)
		}
		for agentID, grant := range grants {
			grantsByAgent[agentID] = grant
		}
	}
	for _, agent := range agents {
		if agent == nil {
			continue
		}
		decisions[agent.ID] = types.DecideAgentAccess(agent, modesByAgent[agent.ID],
			grantsByAgent[agent.ID], caller.TenantID, caller.UserID, caller.Role, systemAdmin)
	}
	return decisions, nil
}
