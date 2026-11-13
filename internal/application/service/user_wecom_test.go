package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// wecomTestUserRepo is the minimal UserRepository + identity store the WeCom
// provisioning path touches.
type wecomTestUserRepo struct {
	interfaces.UserRepository
	users      map[string]*types.User
	identities map[string]*types.UserIdentity
	byEmail    map[string]*types.User
}

func newWeComTestUserRepo() *wecomTestUserRepo {
	return &wecomTestUserRepo{
		users:      map[string]*types.User{},
		identities: map[string]*types.UserIdentity{},
		byEmail:    map[string]*types.User{},
	}
}

func (r *wecomTestUserRepo) CreateUser(_ context.Context, user *types.User) error {
	r.users[user.ID] = user
	return nil
}
func (r *wecomTestUserRepo) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if user, ok := r.users[id]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
}
func (r *wecomTestUserRepo) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if user, ok := r.byEmail[email]; ok {
		return user, nil
	}
	return nil, errors.New("user not found")
}
func (r *wecomTestUserRepo) GetUserByUsername(context.Context, string) (*types.User, error) {
	return nil, errors.New("user not found")
}
func (r *wecomTestUserRepo) UpdateUser(context.Context, *types.User) error { return nil }

func (r *wecomTestUserRepo) GetUserIdentity(_ context.Context, provider, subject string) (*types.UserIdentity, error) {
	if id, ok := r.identities[provider+"|"+subject]; ok {
		return id, nil
	}
	return nil, nil
}
func (r *wecomTestUserRepo) CreateUserIdentity(_ context.Context, identity *types.UserIdentity) error {
	r.identities[identity.Provider+"|"+identity.Subject] = identity
	return nil
}
func (r *wecomTestUserRepo) ListUserIdentities(_ context.Context, userID string) ([]*types.UserIdentity, error) {
	var out []*types.UserIdentity
	for _, id := range r.identities {
		if id.UserID == userID {
			out = append(out, id)
		}
	}
	return out, nil
}

// wecomTestMemberService records AddMember calls.
type wecomTestMemberService struct {
	interfaces.TenantMemberService
	added []struct {
		UserID   string
		TenantID uint64
		Role     types.TenantRole
	}
}

func (m *wecomTestMemberService) AddMember(
	_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string,
) (*types.TenantMember, error) {
	m.added = append(m.added, struct {
		UserID   string
		TenantID uint64
		Role     types.TenantRole
	}{userID, tenantID, role})
	return &types.TenantMember{UserID: userID, TenantID: tenantID, Role: role}, nil
}

func newWeComTestService() (*userService, *wecomTestUserRepo, *wecomTestMemberService) {
	repo := newWeComTestUserRepo()
	members := &wecomTestMemberService{}
	return &userService{
		userRepo:      repo,
		memberService: members,
		config:        &config.Config{},
	}, repo, members
}

// The whole point of JoinTenantID: a first-time member must land in the shared
// workspace with the configured role, not in an empty private one.
func TestProvisionWeComUserJoinsConfiguredTenant(t *testing.T) {
	svc, repo, members := newWeComTestService()

	user, err := svc.provisionWeComUser(context.Background(), "18017873051", types.WeComLoginOptions{
		AutoProvision: true,
		JoinTenantID:  10000,
		Role:          types.TenantRoleViewer,
	})
	require.NoError(t, err)
	require.NotNil(t, user)

	require.Len(t, members.added, 1, "auto-provisioned member must join exactly one workspace")
	require.Equal(t, uint64(10000), members.added[0].TenantID)
	require.Equal(t, types.TenantRoleViewer, members.added[0].Role)
	require.Equal(t, user.ID, members.added[0].UserID)

	// Home tenant is pinned to the join tenant so later logins resolve there.
	require.Equal(t, uint64(10000), user.TenantID)

	// The identity binding is what makes the SECOND login skip provisioning.
	identity, err := repo.GetUserIdentity(context.Background(), types.IdentityProviderWeCom, "18017873051")
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, user.ID, identity.UserID)
	require.Equal(t, 1, len(repo.users))
}

// No join tenant = legacy behaviour: account created, no membership granted.
func TestProvisionWeComUserWithoutJoinTenantGrantsNoMembership(t *testing.T) {
	svc, repo, members := newWeComTestService()

	user, err := svc.provisionWeComUser(context.Background(), "user-2", types.WeComLoginOptions{
		AutoProvision: true,
		Provisioning:  types.TenantProvisioningTenantless,
	})
	require.NoError(t, err)
	require.Empty(t, members.added)
	require.Equal(t, uint64(0), user.TenantID)
	require.Len(t, repo.users, 1)
}

// An invalid/empty role must not produce a membership with an unknown role.
func TestProvisionWeComUserDefaultsRoleToViewer(t *testing.T) {
	svc, _, members := newWeComTestService()

	_, err := svc.provisionWeComUser(context.Background(), "user-3", types.WeComLoginOptions{
		AutoProvision: true,
		JoinTenantID:  10000,
	})
	require.NoError(t, err)
	require.Equal(t, types.TenantRoleViewer, members.added[0].Role)
}

// Unbound + auto-provision disabled must be a clear "ask an admin" result, and
// must never create an account.
func TestLoginWithWeComRefusesUnboundWhenAutoProvisionDisabled(t *testing.T) {
	svc, repo, members := newWeComTestService()

	resp, err := svc.LoginWithWeCom(context.Background(), "18017873051",
		types.WeComLoginOptions{AutoProvision: false})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.False(t, resp.Success)
	require.Contains(t, resp.Message, "尚未绑定")
	require.Empty(t, repo.users, "no account may be created when auto-provision is off")
	require.Empty(t, members.added)
}

// A mailbox from the corporate directory that matches an existing account must
// merge the member into that account: no duplicate, binding written, and the
// existing role/sessions survive.
func TestResolveWeComUserMergesByDirectoryEmail(t *testing.T) {
	svc, repo, members := newWeComTestService()
	existing := &types.User{ID: "user-zhangzy", Username: "zhangzy", Email: "zhangzy@unionclin.com", TenantID: 10000, IsActive: true}
	repo.users[existing.ID] = existing
	repo.byEmail[existing.Email] = existing

	user, isNew, err := svc.resolveWeComUser(context.Background(), "18000000000", types.WeComLoginOptions{
		AutoProvision: true,
		JoinTenantID:  10000,
		Email:         "zhangzy@unionclin.com",
	})
	require.NoError(t, err)
	require.False(t, isNew, "an email match must not create a new account")
	require.Equal(t, existing.ID, user.ID)
	require.Len(t, repo.users, 1, "no duplicate account")
	require.Empty(t, members.added, "existing membership is reused, not re-added")

	identity, err := repo.GetUserIdentity(context.Background(), types.IdentityProviderWeCom, "18000000000")
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, existing.ID, identity.UserID)
}

// A second login by the same member resolves through the binding and never
// re-provisions.
func TestResolveWeComUserUsesExistingBinding(t *testing.T) {
	svc, repo, _ := newWeComTestService()
	bound := &types.User{ID: "user-bound", Username: "chengs", IsActive: true}
	repo.users[bound.ID] = bound
	repo.identities["wecom|18017873051"] = &types.UserIdentity{
		UserID: bound.ID, Provider: types.IdentityProviderWeCom, Subject: "18017873051",
	}

	user, isNew, err := svc.resolveWeComUser(context.Background(), "18017873051", types.WeComLoginOptions{
		AutoProvision: false,
	})
	require.NoError(t, err)
	require.False(t, isNew)
	require.Equal(t, bound.ID, user.ID)
}

// The directory name becomes the username when the contacts permission is
// available, so the UI does not show "wecom-18017873051".
func TestProvisionWeComUserUsesDirectoryNameAndEmail(t *testing.T) {
	svc, repo, _ := newWeComTestService()

	user, err := svc.provisionWeComUser(context.Background(), "18017873051", types.WeComLoginOptions{
		AutoProvision: true,
		JoinTenantID:  10000,
		DisplayName:   "陈国生",
		Email:         "chengs@unionclin.com",
	})
	require.NoError(t, err)
	require.Equal(t, "陈国生", user.Username)
	require.Equal(t, "chengs@unionclin.com", user.Email)
	require.Len(t, repo.users, 1)
}

// Without the contacts permission the account still gets created, just with the
// member account as its name and a synthetic mailbox.
func TestProvisionWeComUserFallsBackWhenDirectoryUnavailable(t *testing.T) {
	svc, _, _ := newWeComTestService()

	user, err := svc.provisionWeComUser(context.Background(), "18017873051", types.WeComLoginOptions{
		AutoProvision: true,
		JoinTenantID:  10000,
	})
	require.NoError(t, err)
	require.Contains(t, user.Username, "18017873051")
	require.Contains(t, user.Email, "@wecom.invalid")
}
