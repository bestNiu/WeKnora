package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// agentAccessRepository stores per-(tenant, agent) visibility policy and the
// per-member allowlist.
//
// A missing policy row means "tenant" (every member sees the agent), so reads
// never create a row. Allowlist rows are soft-deleted: revoking a grant keeps
// the row for audit, and re-granting revives it. The schema enforces one live
// row per (tenant_id, agent_id, user_id) with a partial unique index.
type agentAccessRepository struct {
	db *gorm.DB
}

// NewAgentAccessRepository returns a repository backed by GORM.
func NewAgentAccessRepository(db *gorm.DB) interfaces.AgentAccessRepository {
	return &agentAccessRepository{db: db}
}

// ListPolicies returns the stored modes for the given agents.
func (r *agentAccessRepository) ListPolicies(
	ctx context.Context, tenantID uint64, agentIDs []string,
) (map[string]types.AgentVisibilityMode, error) {
	modes := make(map[string]types.AgentVisibilityMode, len(agentIDs))
	if tenantID == 0 || len(agentIDs) == 0 {
		return modes, nil
	}
	var rows []*types.AgentAccessPolicy
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND agent_id IN ?", tenantID, agentIDs).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list agent access policies: %w", err)
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		modes[row.AgentID] = types.AgentVisibilityMode(row.VisibilityMode).Normalize()
	}
	return modes, nil
}

// GetPolicy returns one agent's mode, defaulting to tenant.
func (r *agentAccessRepository) GetPolicy(
	ctx context.Context, tenantID uint64, agentID string,
) (types.AgentVisibilityMode, error) {
	if agentID == "" {
		return types.AgentVisibilityTenant, errors.New("agent access: agent id is required")
	}
	var row types.AgentAccessPolicy
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
		Limit(1).Find(&row).Error
	if err != nil {
		return types.AgentVisibilityTenant, fmt.Errorf("get agent access policy: %w", err)
	}
	return types.AgentVisibilityMode(row.VisibilityMode).Normalize(), nil
}

// SetPolicy upserts one agent's mode.
func (r *agentAccessRepository) SetPolicy(
	ctx context.Context, tenantID uint64, agentID string,
	mode types.AgentVisibilityMode, updatedBy string,
) error {
	if tenantID == 0 || strings.TrimSpace(agentID) == "" {
		return errors.New("agent access: tenant id and agent id are required")
	}
	now := time.Now()
	row := &types.AgentAccessPolicy{
		TenantID:       tenantID,
		AgentID:        agentID,
		VisibilityMode: string(mode.Normalize()),
		UpdatedBy:      updatedBy,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "tenant_id"}, {Name: "agent_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"visibility_mode", "updated_by", "updated_at",
		}),
	}).Create(row).Error
}

// ListByAgent returns the live grants for one agent.
func (r *agentAccessRepository) ListByAgent(
	ctx context.Context, tenantID uint64, agentID string,
) ([]*types.AgentUserAccess, error) {
	if agentID == "" {
		return nil, errors.New("agent access: agent id is required")
	}
	var rows []*types.AgentUserAccess
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
		Order("created_at ASC, user_id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list agent user access: %w", err)
	}
	return rows, nil
}

// GetByUserAndAgents returns the caller's live grants for the given agents.
func (r *agentAccessRepository) GetByUserAndAgents(
	ctx context.Context, tenantID uint64, userID string, agentIDs []string,
) (map[string]*types.AgentUserAccess, error) {
	byAgent := make(map[string]*types.AgentUserAccess, len(agentIDs))
	if tenantID == 0 || userID == "" || len(agentIDs) == 0 {
		return byAgent, nil
	}
	var rows []*types.AgentUserAccess
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND user_id = ? AND agent_id IN ?", tenantID, userID, agentIDs).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("get agent user access: %w", err)
	}
	for _, row := range rows {
		if row != nil {
			byAgent[row.AgentID] = row
		}
	}
	return byAgent, nil
}

// ReplaceForAgent applies the desired grant set.
func (r *agentAccessRepository) ReplaceForAgent(
	ctx context.Context, tenantID uint64, agentID, grantedBy string,
	grants []types.AgentUserGrant,
) error {
	if agentID == "" {
		return errors.New("agent access: agent id is required")
	}
	normalized, err := normalizeAgentUserGrants(grants)
	if err != nil {
		return err
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing []*types.AgentUserAccess
		if err := tx.Unscoped().
			Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
			Find(&existing).Error; err != nil {
			return fmt.Errorf("load agent user access: %w", err)
		}
		byUser := make(map[string]*types.AgentUserAccess, len(existing))
		for _, row := range existing {
			byUser[row.UserID] = row
		}

		want := make(map[string]types.AgentUserGrant, len(normalized))
		for _, grant := range normalized {
			want[grant.UserID] = grant
		}

		now := time.Now()
		for _, row := range existing {
			if _, keep := want[row.UserID]; keep || row.DeletedAt.Valid {
				continue
			}
			if err := tx.Delete(row).Error; err != nil {
				return fmt.Errorf("revoke agent user access: %w", err)
			}
		}

		for _, grant := range normalized {
			row, ok := byUser[grant.UserID]
			if !ok {
				if err := tx.Create(&types.AgentUserAccess{
					TenantID:  tenantID,
					AgentID:   agentID,
					UserID:    grant.UserID,
					CanView:   grant.CanView,
					GrantedBy: grantedBy,
					CreatedAt: now,
					UpdatedAt: now,
				}).Error; err != nil {
					return fmt.Errorf("create agent user access: %w", err)
				}
				continue
			}
			updates := map[string]any{
				"can_view":   grant.CanView,
				"granted_by": grantedBy,
				"updated_at": now,
				"deleted_at": nil,
			}
			if err := tx.Unscoped().Model(row).Updates(updates).Error; err != nil {
				return fmt.Errorf("update agent user access: %w", err)
			}
		}
		return nil
	})
}

// DeleteByAgent soft-deletes every grant for an agent.
func (r *agentAccessRepository) DeleteByAgent(
	ctx context.Context, tenantID uint64, agentID string,
) error {
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND agent_id = ?", tenantID, agentID).
		Delete(&types.AgentUserAccess{}).Error
}

// normalizeAgentUserGrants validates and de-duplicates a grant set: one row per
// user, always a positive view grant (an allowlist entry is by definition a
// grant; "not visible" is expressed by omitting the user).
func normalizeAgentUserGrants(grants []types.AgentUserGrant) ([]types.AgentUserGrant, error) {
	seen := make(map[string]struct{}, len(grants))
	normalized := make([]types.AgentUserGrant, 0, len(grants))
	for _, grant := range grants {
		userID := strings.TrimSpace(grant.UserID)
		if userID == "" {
			return nil, errors.New("agent access: user id is required")
		}
		if !grant.CanView {
			continue
		}
		if _, dup := seen[userID]; dup {
			continue
		}
		seen[userID] = struct{}{}
		normalized = append(normalized, types.AgentUserGrant{UserID: userID, CanView: true})
	}
	return normalized, nil
}
