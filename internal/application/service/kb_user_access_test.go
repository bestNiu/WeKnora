package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKBUserAccessRepo is a minimal in-memory KBUserAccessRepository.
type fakeKBUserAccessRepo struct {
	rows   map[string]map[string]*types.KBUserAccess // kbID -> userID -> row
	err    error
	lookup int
}

func newFakeKBUserAccessRepo() *fakeKBUserAccessRepo {
	return &fakeKBUserAccessRepo{rows: map[string]map[string]*types.KBUserAccess{}}
}

func (f *fakeKBUserAccessRepo) ListByKB(_ context.Context, _ uint64, kbID string) ([]*types.KBUserAccess, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*types.KBUserAccess, 0, len(f.rows[kbID]))
	for _, row := range f.rows[kbID] {
		out = append(out, row)
	}
	return out, nil
}

func (f *fakeKBUserAccessRepo) GetByUserAndKBs(
	_ context.Context, tenantID uint64, userID string, kbIDs []string,
) (map[string]*types.KBUserAccess, error) {
	f.lookup++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]*types.KBUserAccess{}
	for _, kbID := range kbIDs {
		row := f.rows[kbID][userID]
		if row != nil && row.TenantID == tenantID {
			out[kbID] = row
		}
	}
	return out, nil
}

func (f *fakeKBUserAccessRepo) ReplaceForKB(_ context.Context, tenantID uint64, kbID, grantedBy string, grants []types.KBUserGrant) error {
	if f.err != nil {
		return f.err
	}
	if f.rows[kbID] == nil {
		f.rows[kbID] = map[string]*types.KBUserAccess{}
	}
	for _, grant := range grants {
		f.rows[kbID][grant.UserID] = &types.KBUserAccess{
			TenantID: tenantID, KnowledgeBaseID: kbID, UserID: grant.UserID,
			CanView: grant.CanView || grant.CanDownload, CanDownload: grant.CanDownload,
			GrantedBy: grantedBy,
		}
	}
	return nil
}

func (f *fakeKBUserAccessRepo) DeleteByKB(_ context.Context, _ uint64, kbID string) error {
	delete(f.rows, kbID)
	return f.err
}

func TestDecideKBAccessMatrix(t *testing.T) {
	tenantKB := &types.KnowledgeBase{ID: "kb-tenant", TenantID: 7, CreatorID: "creator", VisibilityMode: "tenant"}
	restrictedKB := &types.KnowledgeBase{ID: "kb-restricted", TenantID: 7, CreatorID: "creator", VisibilityMode: "restricted"}
	legacyKB := &types.KnowledgeBase{ID: "kb-legacy", TenantID: 7, CreatorID: "creator"}
	viewGrant := &types.KBUserAccess{KnowledgeBaseID: "kb-restricted", UserID: "viewer", CanView: true}
	downloadGrant := &types.KBUserAccess{KnowledgeBaseID: "kb-restricted", UserID: "viewer", CanView: true, CanDownload: true}

	cases := []struct {
		name         string
		kb           *types.KnowledgeBase
		grant        *types.KBUserAccess
		userID       string
		role         types.TenantRole
		system       bool
		wantView     bool
		wantDownload bool
	}{
		{name: "nil kb", kb: nil, userID: "viewer", role: types.TenantRoleViewer},
		{name: "tenant mode viewer", kb: tenantKB, userID: "viewer", role: types.TenantRoleViewer, wantView: true},
		{name: "tenant mode contributor no longer downloads", kb: tenantKB, userID: "viewer", role: types.TenantRoleContributor, wantView: true},
		{name: "legacy empty mode is tenant", kb: legacyKB, userID: "viewer", role: types.TenantRoleViewer, wantView: true},
		{name: "restricted unlisted", kb: restrictedKB, userID: "viewer", role: types.TenantRoleViewer},
		{name: "restricted listed view", kb: restrictedKB, grant: viewGrant, userID: "viewer", role: types.TenantRoleViewer, wantView: true},
		{name: "restricted listed download", kb: restrictedKB, grant: downloadGrant, userID: "viewer", role: types.TenantRoleViewer, wantView: true, wantDownload: true},
		{name: "unbound principal restricted", kb: restrictedKB, grant: viewGrant, userID: "", role: types.TenantRoleViewer},
		{name: "unbound principal tenant mode", kb: tenantKB, userID: "", role: types.TenantRoleViewer, wantView: true},
		{name: "creator bypasses restriction", kb: restrictedKB, userID: "creator", role: types.TenantRoleViewer, wantView: true, wantDownload: true},
		{name: "admin bypasses restriction", kb: restrictedKB, userID: "admin", role: types.TenantRoleAdmin, wantView: true, wantDownload: true},
		{name: "owner bypasses restriction", kb: restrictedKB, userID: "owner", role: types.TenantRoleOwner, wantView: true, wantDownload: true},
		{name: "system admin bypasses restriction", kb: restrictedKB, userID: "someone", role: types.TenantRoleViewer, system: true, wantView: true, wantDownload: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := types.DecideKBAccess(tc.kb, tc.grant, tc.userID, tc.role, tc.system)
			assert.Equal(t, tc.wantView, got.CanView, "can_view")
			assert.Equal(t, tc.wantDownload, got.CanDownload, "can_download")
		})
	}
}

func TestKBUserAccessServiceDecideManyBatchesPerTenant(t *testing.T) {
	repo := newFakeKBUserAccessRepo()
	svc := NewKBUserAccessService(repo)
	ctx := context.Background()

	require.NoError(t, repo.ReplaceForKB(ctx, 7, "kb-1", "admin", []types.KBUserGrant{
		{UserID: "u1", CanDownload: true},
	}))
	require.NoError(t, repo.ReplaceForKB(ctx, 8, "kb-2", "admin", []types.KBUserGrant{
		{UserID: "u1", CanView: true},
	}))

	kbs := []*types.KnowledgeBase{
		{ID: "kb-1", TenantID: 7, VisibilityMode: "restricted"},
		{ID: "kb-2", TenantID: 8, VisibilityMode: "restricted"},
		{ID: "kb-3", TenantID: 7, VisibilityMode: "restricted"},
	}
	decisions, err := svc.DecideMany(ctx, kbs, "u1", types.TenantRoleViewer, false)
	require.NoError(t, err)
	assert.True(t, decisions["kb-1"].CanView)
	assert.True(t, decisions["kb-1"].CanDownload)
	assert.True(t, decisions["kb-2"].CanView)
	assert.False(t, decisions["kb-2"].CanDownload)
	assert.False(t, decisions["kb-3"].CanView)
	assert.Equal(t, 2, repo.lookup, "one lookup per owning tenant")
}

func TestKBUserAccessServiceEmptyPrincipalSkipsLookup(t *testing.T) {
	repo := newFakeKBUserAccessRepo()
	svc := NewKBUserAccessService(repo)
	ctx := context.Background()

	restricted := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, VisibilityMode: "restricted"}
	decision, err := svc.Decide(ctx, restricted, "", types.TenantRoleViewer, false)
	require.NoError(t, err)
	assert.False(t, decision.CanView)
	assert.Equal(t, 0, repo.lookup, "an unbound principal has nothing to look up")

	tenantWide := &types.KnowledgeBase{ID: "kb-2", TenantID: 7, VisibilityMode: "tenant"}
	decision, err = svc.Decide(ctx, tenantWide, "", types.TenantRoleViewer, false)
	require.NoError(t, err)
	assert.True(t, decision.CanView)
	assert.False(t, decision.CanDownload)
}

func TestKBUserAccessServicePropagatesLookupErrors(t *testing.T) {
	repo := newFakeKBUserAccessRepo()
	repo.err = errors.New("db down")
	svc := NewKBUserAccessService(repo)
	_, err := svc.Decide(context.Background(),
		&types.KnowledgeBase{ID: "kb-1", TenantID: 7}, "u1", types.TenantRoleViewer, false)
	require.Error(t, err)
}
