package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubMediaVisibility answers from a fixed map and counts batch calls.
type stubVisibilityResolver struct {
	decisions map[string]types.KBAccessDecision
	calls     int
}

func (s *stubVisibilityResolver) DecideKBIDs(
	_ context.Context, kbIDs []string, _ string, _ types.TenantRole, _ bool,
) (map[string]types.KBAccessDecision, error) {
	s.calls++
	out := make(map[string]types.KBAccessDecision, len(kbIDs))
	for _, id := range kbIDs {
		out[id] = s.decisions[id]
	}
	return out, nil
}

func TestFilterOwnTenantKBsByVisibilityKeepsForeignKBs(t *testing.T) {
	resolver := &stubVisibilityResolver{decisions: map[string]types.KBAccessDecision{
		"kb-open":       {CanView: true},
		"kb-restricted": {CanView: false},
		"kb-foreign":    {CanView: false},
	}}
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u1", Role: types.TenantRoleViewer,
	})
	ctx = access.WithKBUserVisibilityResolver(ctx, resolver)

	kbs := []*types.KnowledgeBase{
		{ID: "kb-open", TenantID: 7},
		{ID: "kb-restricted", TenantID: 7, VisibilityMode: "restricted"},
		{ID: "kb-foreign", TenantID: 8, VisibilityMode: "restricted"},
	}
	filtered, err := filterOwnTenantKBsByVisibility(ctx, kbs)
	require.NoError(t, err)
	require.Len(t, filtered, 2)
	assert.Equal(t, "kb-open", filtered[0].ID)
	assert.Equal(t, "kb-foreign", filtered[1].ID, "another workspace's KB keeps share semantics")
	assert.Equal(t, 1, resolver.calls, "one batch lookup for the whole list")
}

func TestFilterOwnTenantKBsByVisibilityWithoutOwnKBsSkipsLookup(t *testing.T) {
	resolver := &stubVisibilityResolver{}
	ctx := types.WithCaller(context.Background(), types.Caller{
		TenantID: 7, UserID: "u1", Role: types.TenantRoleViewer,
	})
	ctx = access.WithKBUserVisibilityResolver(ctx, resolver)

	kbs := []*types.KnowledgeBase{{ID: "kb-foreign", TenantID: 8}}
	filtered, err := filterOwnTenantKBsByVisibility(ctx, kbs)
	require.NoError(t, err)
	assert.Equal(t, kbs, filtered)
	assert.Equal(t, 0, resolver.calls)
}
