package access

import (
	"context"
	"errors"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeVisibilityResolver records calls and answers from a fixed map.
type fakeVisibilityResolver struct {
	decisions map[string]types.KBAccessDecision
	calls     int
	lastIDs   []string
	err       error
}

func (f *fakeVisibilityResolver) DecideKBIDs(
	_ context.Context, kbIDs []string, _ string, _ types.TenantRole, _ bool,
) (map[string]types.KBAccessDecision, error) {
	f.calls++
	f.lastIDs = append([]string(nil), kbIDs...)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]types.KBAccessDecision, len(kbIDs))
	for _, id := range kbIDs {
		out[id] = f.decisions[id]
	}
	return out, nil
}

// fakeShareLookup grants one fixed organization role.
type fakeShareLookup struct {
	role   types.OrgMemberRole
	shared bool
	err    error
}

func (f *fakeShareLookup) CheckTenantKBPermission(
	context.Context, string, uint64, types.TenantRole,
) (types.OrgMemberRole, bool, error) {
	return f.role, f.shared, f.err
}

func callerCtx(userID string, tenantID uint64, role types.TenantRole) context.Context {
	return types.WithCaller(context.Background(), types.Caller{
		TenantID: tenantID, UserID: userID, Role: role,
	})
}

func TestKBPermissionsCheckAppliesOwnTenantVisibility(t *testing.T) {
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-hidden": {CanView: false},
		"kb-open":   {CanView: true},
	}}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	permissions := NewKBPermissions(ctx, nil)

	allowed, err := permissions.Check("kb-hidden", 7, types.OrgRoleViewer)
	require.NoError(t, err)
	assert.False(t, allowed)

	// The decision is cached for the operation: a page of documents from one
	// KB does not repeat the grant lookup.
	allowed, err = permissions.Check("kb-hidden", 7, types.OrgRoleViewer)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, 1, resolver.calls)

	allowed, err = permissions.Check("kb-open", 7, types.OrgRoleViewer)
	require.NoError(t, err)
	assert.True(t, allowed)

	// Organization-shared KBs keep the share semantics; the own-tenant
	// visibility resolver is not consulted for them (no shares configured
	// here, so the foreign KB is simply denied).
	allowed, err = permissions.Check("kb-foreign", 9, types.OrgRoleViewer)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Equal(t, 2, resolver.calls, "only the two own-tenant KBs reached the resolver")
}

func TestKBPermissionsCheckWithoutResolverKeepsLegacyBehaviour(t *testing.T) {
	permissions := NewKBPermissions(callerCtx("u1", 7, types.TenantRoleViewer), nil)
	allowed, err := permissions.Check("kb-1", 7, types.OrgRoleViewer)
	require.NoError(t, err)
	assert.True(t, allowed, "an uninstalled resolver must not hide tenant KBs")
}

func TestKBPermissionsCheckPropagatesResolverError(t *testing.T) {
	resolver := &fakeVisibilityResolver{err: errors.New("db down")}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	_, err := NewKBPermissions(ctx, nil).Check("kb-1", 7, types.OrgRoleViewer)
	require.Error(t, err)
}

func TestResolveKBHidesRestrictedKBFromUnlistedUser(t *testing.T) {
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-hidden": {CanView: false},
	}}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	kb := &types.KnowledgeBase{ID: "kb-hidden", TenantID: 7, VisibilityMode: "restricted"}

	_, err := ResolveKB(ctx, KBRequest{Caller: types.CallerFromContext(ctx)}, kb, types.OrgRoleViewer, nil, nil)
	require.ErrorIs(t, err, ErrNotFound, "a hidden KB answers exactly like a missing one")

	resolver.decisions["kb-hidden"] = types.KBAccessDecision{CanView: true}
	grant, err := ResolveKB(ctx, KBRequest{Caller: types.CallerFromContext(ctx)}, kb, types.OrgRoleViewer, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, grant)
	assert.Equal(t, types.OrgRoleAdmin, grant.Permission)
}

func TestAuthorizeKBAccessRejectsRestrictedOwnTenantKB(t *testing.T) {
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-1": {CanView: false},
	}}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	kbs := []*types.KnowledgeBase{{ID: "kb-1", TenantID: 7, VisibilityMode: "restricted"}}

	err := AuthorizeKBAccess(ctx, nil, kbs)
	require.Error(t, err)
	var appErr *apperrors.AppError
	require.ErrorAs(t, err, &appErr)
	assert.Equal(t, 404, appErr.HTTPCode)
}

func TestAuthorizeKBAccessLeavesOrganizationSharedKBAlone(t *testing.T) {
	// A restricted KB from another workspace is governed by the share grant,
	// not by the caller's own per-user ACL: it must stay readable.
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-foreign": {CanView: false},
	}}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	kbs := []*types.KnowledgeBase{{ID: "kb-foreign", TenantID: 8, VisibilityMode: "restricted"}}
	shares := &fakeShareLookup{role: types.OrgRoleViewer, shared: true}

	require.NoError(t, AuthorizeKBAccess(ctx, shares, kbs))
	assert.Equal(t, 0, resolver.calls, "foreign KBs never reach the per-user resolver")
}

func TestDecideKBVisibilityManyBatchesAndFilterPreservesOrder(t *testing.T) {
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-1": {CanView: true},
		"kb-2": {CanView: false},
		"kb-3": {CanView: true},
	}}
	ctx := WithKBUserVisibilityResolver(callerCtx("u1", 7, types.TenantRoleViewer), resolver)
	kbs := []*types.KnowledgeBase{{ID: "kb-1"}, {ID: "kb-2"}, {ID: "kb-3"}}

	visible, err := FilterVisibleKBs(ctx, kbs)
	require.NoError(t, err)
	require.Len(t, visible, 2)
	assert.Equal(t, []*types.KnowledgeBase{kbs[0], kbs[2]}, visible)
	assert.Len(t, resolver.lastIDs, 3, "one batch call for the whole page")
	assert.Equal(t, 1, resolver.calls)
}

// A machine principal's synthetic role (a full-access API key is "Owner" so
// legacy role guards pass) must not grant knowledge-base access. The bound
// owner is the subject; an unbound key only reaches tenant-wide KBs.
func TestMachinePrincipalRoleDoesNotBypassRestrictedKB(t *testing.T) {
	restricted := &types.KnowledgeBase{ID: "kb-r", TenantID: 7, VisibilityMode: "restricted"}
	tenantWide := &types.KnowledgeBase{ID: "kb-t", TenantID: 7, VisibilityMode: "tenant"}

	unbound := types.WithTenantAPIKeyScope(
		callerCtx("", 7, types.TenantRoleOwner),
		types.TenantAPIKeyScope{KeyID: 1, FullAccess: true},
	)
	decision, err := DecideKBVisibility(unbound, restricted, types.CallerFromContext(unbound))
	require.NoError(t, err)
	assert.False(t, decision.CanView, "an unbound full-access key must not see restricted KBs")
	assert.False(t, decision.CanDownload)

	decision, err = DecideKBVisibility(unbound, tenantWide, types.CallerFromContext(unbound))
	require.NoError(t, err)
	assert.True(t, decision.CanView)
	assert.False(t, decision.CanDownload, "a machine principal never downloads by role")

	// Bound to an owner with an explicit grant: the grant (not the synthetic
	// Owner role) decides.
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-r": {CanView: true, CanDownload: true},
	}}
	bound := types.WithTenantAPIKeyScope(
		WithKBUserVisibilityResolver(callerCtx("owner-1", 7, types.TenantRoleOwner), resolver),
		types.TenantAPIKeyScope{KeyID: 2, OwnerUserID: "owner-1"},
	)
	decision, err = DecideKBVisibility(bound, restricted, types.CallerFromContext(bound))
	require.NoError(t, err)
	assert.True(t, decision.CanView)
	assert.True(t, decision.CanDownload)
}

// Public embed routes and IM callbacks are registered before the /api/v1 auth
// group, so they never carry the request-scoped resolver. The process-wide
// fallback must still enforce per-user grants there.
func TestDefaultResolverAppliesWithoutMiddleware(t *testing.T) {
	resolver := &fakeVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-r": {CanView: true, CanDownload: true},
	}}
	SetDefaultKBUserVisibilityResolver(resolver)
	t.Cleanup(func() { SetDefaultKBUserVisibilityResolver(nil) })

	restricted := &types.KnowledgeBase{ID: "kb-r", TenantID: 7, VisibilityMode: "restricted"}
	ctx := callerCtx("u1", 7, types.TenantRoleViewer) // no WithKBUserVisibilityResolver
	decision, err := DecideKBVisibility(ctx, restricted, types.CallerFromContext(ctx))
	require.NoError(t, err)
	assert.True(t, decision.CanView)
	assert.True(t, decision.CanDownload)
}
