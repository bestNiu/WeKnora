package access

import (
	"context"
	"sync/atomic"

	"github.com/Tencent/WeKnora/internal/types"
)

// KBUserVisibilityResolver answers "may this user read this knowledge base?"
// for the per-user ACL that sits on top of tenant roles and organization
// shares.
//
// It is injected into the request context by the HTTP layer because the answer
// needs a database lookup (the KB's visibility mode and the user's grants) that
// the access package must not own. Contexts without a resolver — background
// workers, cleanup jobs — keep the pre-ACL behaviour: the caller identity has no
// user, so restricted KBs stay invisible and tenant-wide KBs stay readable.
type KBUserVisibilityResolver interface {
	// DecideKBIDs resolves the decision for the requested knowledge base IDs.
	// A missing map entry is not visible. The implementation is expected to
	// batch, so callers may pass every ID of a page.
	DecideKBIDs(
		ctx context.Context,
		kbIDs []string,
		callerUserID string,
		callerRole types.TenantRole,
		systemAdmin bool,
	) (map[string]types.KBAccessDecision, error)
}

type kbUserVisibilityContextKey struct{}

// resolverHolder lets atomic.Pointer hold an interface value.
type resolverHolder struct {
	resolver KBUserVisibilityResolver
}

// defaultVisibilityResolver covers surfaces whose requests do not pass through
// the /api/v1 middleware: the public embed routes and the IM callbacks are
// registered on the engine before auth, so they have no request resolver. The
// container installs the same resolver process-wide; bound owners keep working
// there instead of falling back to the no-grant legacy rule.
var defaultVisibilityResolver atomic.Pointer[resolverHolder]

// SetDefaultKBUserVisibilityResolver installs (or clears) the process-wide
// fallback. It is read-only after startup.
func SetDefaultKBUserVisibilityResolver(resolver KBUserVisibilityResolver) {
	if resolver == nil {
		defaultVisibilityResolver.Store(nil)
		return
	}
	defaultVisibilityResolver.Store(&resolverHolder{resolver: resolver})
}

// WithKBUserVisibilityResolver installs the per-request visibility authority.
// Middleware sets it once per authenticated request; nested calls inherit it.
func WithKBUserVisibilityResolver(ctx context.Context, resolver KBUserVisibilityResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, kbUserVisibilityContextKey{}, resolver)
}

// KBUserVisibilityResolverFromContext returns the installed resolver, if any.
func KBUserVisibilityResolverFromContext(ctx context.Context) (KBUserVisibilityResolver, bool) {
	if resolver, ok := ctx.Value(kbUserVisibilityContextKey{}).(KBUserVisibilityResolver); ok && resolver != nil {
		return resolver, true
	}
	if holder := defaultVisibilityResolver.Load(); holder != nil && holder.resolver != nil {
		return holder.resolver, true
	}
	return nil, false
}

// CanDownloadKB reports whether the caller may download the KB's original
// files.
//
// Download is independent of read access: a Viewer can preview without being
// allowed to take the source file. Only Owner/Admin, the KB creator and an
// explicit can_download grant qualify. Organization-shared KBs keep their
// existing rule at the route guard, so callers must only apply this to a KB
// owned by the caller's workspace.
func CanDownloadKB(ctx context.Context, kb *types.KnowledgeBase) (bool, error) {
	if kb == nil {
		return false, nil
	}
	decision, err := DecideKBVisibility(ctx, kb, types.CallerFromContext(ctx))
	if err != nil {
		return false, err
	}
	return decision.CanDownload, nil
}

// aclCaller returns the identity the per-user ACL evaluates.
//
// Machine principals (API keys, channel bots) carry a synthetic tenant role: a
// full-access API key is "Owner" so legacy role guards pass. That role must not
// grant knowledge-base access. The bound owner is the subject, and only their
// explicit grants and creator status apply — an unbound principal therefore
// only reaches tenant-wide KBs.
func aclCaller(ctx context.Context, caller types.Caller) types.Caller {
	if _, ok := types.TenantAPIKeyScopeFromContext(ctx); ok {
		caller.Role = types.TenantRoleViewer
	}
	return caller
}

// DecideKBVisibility resolves visibility for KB objects already in hand.
//
// Without a resolver it falls back to the pure rule in types.DecideKBAccess,
// which honours visibility mode and ownership but cannot see grants. That keeps
// non-HTTP contexts working and is the fail-closed path for restricted KBs.
func DecideKBVisibility(
	ctx context.Context,
	kb *types.KnowledgeBase,
	caller types.Caller,
) (types.KBAccessDecision, error) {
	if kb == nil {
		return types.KBAccessDecision{}, nil
	}
	caller = aclCaller(ctx, caller.Normalize())
	systemAdmin := types.IsSystemAdminFromContext(ctx)
	resolver, ok := KBUserVisibilityResolverFromContext(ctx)
	if !ok || caller.UserID == "" {
		return types.DecideKBAccess(kb, nil, caller.UserID, caller.Role, systemAdmin), nil
	}
	decisions, err := resolver.DecideKBIDs(ctx, []string{kb.ID}, caller.UserID, caller.Role, systemAdmin)
	if err != nil {
		return types.KBAccessDecision{}, err
	}
	return decisions[kb.ID], nil
}

// DecideKBVisibilityMany is the batch form for list and search paths.
// Callers pass the KBs they already loaded; the resolver batches the grant and
// visibility lookups across them.
func DecideKBVisibilityMany(
	ctx context.Context,
	kbs []*types.KnowledgeBase,
	caller types.Caller,
) (map[string]types.KBAccessDecision, error) {
	decisions := make(map[string]types.KBAccessDecision, len(kbs))
	if len(kbs) == 0 {
		return decisions, nil
	}
	caller = aclCaller(ctx, caller.Normalize())
	systemAdmin := types.IsSystemAdminFromContext(ctx)
	resolver, ok := KBUserVisibilityResolverFromContext(ctx)
	if !ok || caller.UserID == "" {
		for _, kb := range kbs {
			if kb != nil {
				decisions[kb.ID] = types.DecideKBAccess(kb, nil, caller.UserID, caller.Role, systemAdmin)
			}
		}
		return decisions, nil
	}
	ids := make([]string, 0, len(kbs))
	for _, kb := range kbs {
		if kb != nil && kb.ID != "" {
			ids = append(ids, kb.ID)
		}
	}
	resolved, err := resolver.DecideKBIDs(ctx, ids, caller.UserID, caller.Role, systemAdmin)
	if err != nil {
		return nil, err
	}
	for _, kb := range kbs {
		if kb != nil {
			decisions[kb.ID] = resolved[kb.ID]
		}
	}
	return decisions, nil
}

// FilterVisibleKBs returns the KBs the caller may read, preserving order.
// A resolver error fails the whole call: guessing would either leak a
// restricted KB or hide a readable one.
func FilterVisibleKBs(
	ctx context.Context,
	kbs []*types.KnowledgeBase,
) ([]*types.KnowledgeBase, error) {
	decisions, err := DecideKBVisibilityMany(ctx, kbs, types.CallerFromContext(ctx))
	if err != nil {
		return nil, err
	}
	visible := make([]*types.KnowledgeBase, 0, len(kbs))
	for _, kb := range kbs {
		if kb != nil && decisions[kb.ID].CanView {
			visible = append(visible, kb)
		}
	}
	return visible, nil
}
