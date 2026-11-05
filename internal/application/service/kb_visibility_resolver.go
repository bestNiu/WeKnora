package service

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// kbVisibilityResolver adapts the per-user KB ACL to the id-based interface the
// access package installs on a request context.
//
// The access package knows only IDs at several choke points (batch documents,
// chunks, message references). This adapter loads the KB rows in one query and
// delegates the decision to KBUserAccessService, which batches the grant lookup
// per owning tenant.
type kbVisibilityResolver struct {
	kbService     interfaces.KnowledgeBaseService
	accessService interfaces.KBUserAccessService
}

// NewKBVisibilityResolver builds the resolver consumed by access middleware.
func NewKBVisibilityResolver(
	kbService interfaces.KnowledgeBaseService,
	accessService interfaces.KBUserAccessService,
) access.KBUserVisibilityResolver {
	return &kbVisibilityResolver{kbService: kbService, accessService: accessService}
}

// DecideKBIDs resolves visibility for the requested knowledge bases.
func (r *kbVisibilityResolver) DecideKBIDs(
	ctx context.Context,
	kbIDs []string,
	callerUserID string,
	callerRole types.TenantRole,
	systemAdmin bool,
) (map[string]types.KBAccessDecision, error) {
	decisions := make(map[string]types.KBAccessDecision, len(kbIDs))
	if len(kbIDs) == 0 || r == nil || r.kbService == nil || r.accessService == nil {
		return decisions, nil
	}
	kbs, err := r.kbService.GetKnowledgeBasesByIDsOnly(ctx, kbIDs)
	if err != nil {
		return nil, err
	}
	return r.accessService.DecideMany(ctx, kbs, callerUserID, callerRole, systemAdmin)
}
