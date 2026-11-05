package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbUserAccessService resolves per-user knowledge-base access and owns the
// admin-side grant management.
//
// Resolution is request-local: every call reads the current rows, so a revoked
// grant takes effect on the next request. There is deliberately no process-wide
// cache; the existing access package caches only within one operation.
type kbUserAccessService struct {
	repo interfaces.KBUserAccessRepository
}

// NewKBUserAccessService returns an access service backed by repo.
func NewKBUserAccessService(repo interfaces.KBUserAccessRepository) interfaces.KBUserAccessService {
	return &kbUserAccessService{repo: repo}
}

// Resolve returns the caller's live grants for the given KBs.
func (s *kbUserAccessService) Resolve(
	ctx context.Context, tenantID uint64, userID string, kbIDs []string,
) (map[string]*types.KBUserAccess, error) {
	if s == nil || s.repo == nil {
		return map[string]*types.KBUserAccess{}, nil
	}
	return s.repo.GetByUserAndKBs(ctx, tenantID, userID, kbIDs)
}

// ListByKB lists one KB's grants for the admin UI.
func (s *kbUserAccessService) ListByKB(
	ctx context.Context, tenantID uint64, kbID string,
) ([]*types.KBUserAccess, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("kb user access service is unavailable")
	}
	return s.repo.ListByKB(ctx, tenantID, kbID)
}

// ReplaceForKB applies the desired grant set.
func (s *kbUserAccessService) ReplaceForKB(
	ctx context.Context, tenantID uint64, kbID, grantedBy string, grants []types.KBUserGrant,
) error {
	if s == nil || s.repo == nil {
		return errors.New("kb user access service is unavailable")
	}
	if tenantID == 0 {
		return errors.New("kb user access: tenant id is required")
	}
	if strings.TrimSpace(kbID) == "" {
		return errors.New("kb user access: knowledge base id is required")
	}
	return s.repo.ReplaceForKB(ctx, tenantID, kbID, grantedBy, grants)
}

// DeleteByKB drops every grant for a KB.
func (s *kbUserAccessService) DeleteByKB(
	ctx context.Context, tenantID uint64, kbID string,
) error {
	if s == nil || s.repo == nil {
		return errors.New("kb user access service is unavailable")
	}
	return s.repo.DeleteByKB(ctx, tenantID, kbID)
}

// Decide resolves view and download for one caller and KB.
func (s *kbUserAccessService) Decide(
	ctx context.Context,
	kb *types.KnowledgeBase,
	callerUserID string,
	callerRole types.TenantRole,
	systemAdmin bool,
) (types.KBAccessDecision, error) {
	if kb == nil {
		return types.KBAccessDecision{}, nil
	}
	if callerUserID == "" || s == nil || s.repo == nil {
		return types.DecideKBAccess(kb, nil, callerUserID, callerRole, systemAdmin), nil
	}
	grants, err := s.repo.GetByUserAndKBs(ctx, kb.TenantID, callerUserID, []string{kb.ID})
	if err != nil {
		return types.KBAccessDecision{}, fmt.Errorf("resolve kb access: %w", err)
	}
	return types.DecideKBAccess(kb, grants[kb.ID], callerUserID, callerRole, systemAdmin), nil
}

// DecideMany resolves the same decision for several KBs with one grant lookup
// per owning tenant.
func (s *kbUserAccessService) DecideMany(
	ctx context.Context,
	kbs []*types.KnowledgeBase,
	callerUserID string,
	callerRole types.TenantRole,
	systemAdmin bool,
) (map[string]types.KBAccessDecision, error) {
	decisions := make(map[string]types.KBAccessDecision, len(kbs))
	if len(kbs) == 0 {
		return decisions, nil
	}
	if callerUserID == "" || s == nil || s.repo == nil {
		for _, kb := range kbs {
			if kb != nil {
				decisions[kb.ID] = types.DecideKBAccess(kb, nil, callerUserID, callerRole, systemAdmin)
			}
		}
		return decisions, nil
	}

	// Grants are tenant-scoped, and a shared KB belongs to a different tenant
	// than the caller's. Group the lookup by owning tenant so one query per
	// tenant resolves the whole page.
	idsByTenant := make(map[uint64][]string)
	for _, kb := range kbs {
		if kb != nil {
			idsByTenant[kb.TenantID] = append(idsByTenant[kb.TenantID], kb.ID)
		}
	}
	grantsByKB := make(map[string]*types.KBUserAccess, len(kbs))
	for tenantID, ids := range idsByTenant {
		grants, err := s.repo.GetByUserAndKBs(ctx, tenantID, callerUserID, ids)
		if err != nil {
			return nil, fmt.Errorf("resolve kb access: %w", err)
		}
		for kbID, grant := range grants {
			grantsByKB[kbID] = grant
		}
	}
	for _, kb := range kbs {
		if kb == nil {
			continue
		}
		decisions[kb.ID] = types.DecideKBAccess(kb, grantsByKB[kb.ID], callerUserID, callerRole, systemAdmin)
	}
	return decisions, nil
}
