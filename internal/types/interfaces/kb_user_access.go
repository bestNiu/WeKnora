package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// KBUserAccessRepository persists per-(knowledge base, user) access overrides.
//
// Every method is tenant-scoped: a row is only readable or replaceable through
// the tenant that owns the knowledge base. Callers resolve the KB first
// (which already proves tenant visibility) and pass its TenantID.
type KBUserAccessRepository interface {
	// ListByKB returns the live grants for one knowledge base.
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.KBUserAccess, error)
	// GetByUserAndKBs returns the caller's live grants for the given KBs,
	// keyed by knowledge base ID. Unknown IDs are simply absent.
	GetByUserAndKBs(ctx context.Context, tenantID uint64, userID string, kbIDs []string) (map[string]*types.KBUserAccess, error)
	// ReplaceForKB makes the stored grant set match grants exactly: missing
	// users are soft-deleted, present users are created or revived. The
	// operation is transactional and idempotent.
	ReplaceForKB(ctx context.Context, tenantID uint64, kbID, grantedBy string, grants []types.KBUserGrant) error
	// DeleteByKB soft-deletes every grant for a knowledge base.
	DeleteByKB(ctx context.Context, tenantID uint64, kbID string) error
}

// KBUserAccessService is the application-layer view of per-user KB access.
//
// It also owns the effective decision for one caller and KB, so HTTP handlers,
// retrieval and the ACL resolver share a single rule instead of re-implementing
// it. Machine principals pass their bound owner as callerUserID.
type KBUserAccessService interface {
	// Resolve returns the caller's live grants for the given KBs.
	Resolve(ctx context.Context, tenantID uint64, userID string, kbIDs []string) (map[string]*types.KBUserAccess, error)
	// ListByKB lists one KB's grants for the admin UI.
	ListByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.KBUserAccess, error)
	// ReplaceForKB applies the desired grant set.
	ReplaceForKB(ctx context.Context, tenantID uint64, kbID, grantedBy string, grants []types.KBUserGrant) error
	// DeleteByKB drops every grant for a KB (used when the KB is deleted).
	DeleteByKB(ctx context.Context, tenantID uint64, kbID string) error
	// Decide resolves view and download for one caller and KB.
	Decide(
		ctx context.Context,
		kb *types.KnowledgeBase,
		callerUserID string,
		callerRole types.TenantRole,
		systemAdmin bool,
	) (types.KBAccessDecision, error)
	// DecideMany resolves the same decision for several KBs with one grant
	// lookup. The result is keyed by knowledge base ID; a KB missing from the
	// map is not visible.
	DecideMany(
		ctx context.Context,
		kbs []*types.KnowledgeBase,
		callerUserID string,
		callerRole types.TenantRole,
		systemAdmin bool,
	) (map[string]types.KBAccessDecision, error)
}
