package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type agentAccessAgentStub struct {
	interfaces.CustomAgentService
	agent *types.CustomAgent
}

func (s *agentAccessAgentStub) GetAgentByID(context.Context, string) (*types.CustomAgent, error) {
	return s.agent, nil
}

type agentAccessMemberStub struct {
	interfaces.TenantMemberService
	members []*types.TenantMember
	queried string
}

func (s *agentAccessMemberStub) ListMembersPage(
	_ context.Context, _ uint64, query string, _, _ int,
) ([]*types.TenantMember, int64, error) {
	s.queried = query
	return s.members, int64(len(s.members)), nil
}

type agentAccessUserStub struct {
	interfaces.UserService
	users map[string]*types.User
}

func (s *agentAccessUserStub) GetUsersByIDs(context.Context, []string) (map[string]*types.User, error) {
	return s.users, nil
}

type agentAccessGrantStub struct {
	interfaces.AgentAccessService
	mode        types.AgentVisibilityMode
	rows        []*types.AgentUserAccess
	patch       []types.AgentUserGrant
	patchCalled bool
	replaced    []types.AgentUserGrant
	savedMode   types.AgentVisibilityMode
}

func (s *agentAccessGrantStub) ListByAgent(context.Context, uint64, string) ([]*types.AgentUserAccess, error) {
	return s.rows, nil
}

func (s *agentAccessGrantStub) Policy(context.Context, uint64, string) (types.AgentVisibilityMode, error) {
	return s.mode, nil
}

func (s *agentAccessGrantStub) SetPolicy(
	_ context.Context, _ uint64, _ string, mode types.AgentVisibilityMode, _ string,
) error {
	s.savedMode = mode
	return nil
}

func (s *agentAccessGrantStub) UpsertForAgent(
	_ context.Context, _ uint64, _ string, _ string, grants []types.AgentUserGrant,
) error {
	s.patchCalled = true
	s.patch = grants
	return nil
}

func (s *agentAccessGrantStub) ReplaceForAgent(
	_ context.Context, _ uint64, _ string, _ string, grants []types.AgentUserGrant,
) error {
	s.replaced = grants
	return nil
}

func agentAccessFixture() (*AgentAccessHandler, *agentAccessGrantStub, *agentAccessMemberStub, *agentAccessAgentStub) {
	agent := &types.CustomAgent{ID: "agent-1", TenantID: 10042, CreatedBy: "creator-1"}
	grants := &agentAccessGrantStub{
		mode: types.AgentVisibilityAllowlist,
		rows: []*types.AgentUserAccess{
			{AgentID: "agent-1", UserID: "granted-1", CanView: true},
			{AgentID: "agent-1", UserID: "granted-2", CanView: true},
		},
	}
	members := &agentAccessMemberStub{members: []*types.TenantMember{
		{UserID: "creator-1", Role: types.TenantRoleContributor, Status: types.TenantMemberStatusActive},
		{UserID: "admin-1", Role: types.TenantRoleAdmin, Status: types.TenantMemberStatusActive},
		{UserID: "granted-1", Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
		{UserID: "other-1", Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended},
	}}
	users := &agentAccessUserStub{users: map[string]*types.User{
		"granted-1": {ID: "granted-1", Username: "Alice", Email: "alice@example.com"},
	}}
	agents := &agentAccessAgentStub{agent: agent}
	h := NewAgentAccessHandler(agents, grants, members, users)
	return h, grants, members, agents
}

func agentAccessContext(method, target string, body []byte, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if body != nil {
		c.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
	} else {
		c.Request = httptest.NewRequest(method, target, nil)
	}
	c.Request = c.Request.WithContext(types.WithCaller(c.Request.Context(), types.Caller{
		TenantID: 10042, UserID: "admin-1", Role: types.TenantRoleAdmin,
	}))
	c.Params = params
	return c, w
}

// The dialog pages over members, so the response must carry identity, role and
// the implicit-creator/admin marker instead of only raw user IDs.
func TestGetAgentAccessReturnsPageWithIdentity(t *testing.T) {
	h, _, members, _ := agentAccessFixture()
	c, w := agentAccessContext(http.MethodGet, "/api/v1/agents/agent-1/access?page=2&page_size=20&q=ali",
		nil, gin.Params{{Key: "id", Value: "agent-1"}})
	h.GetAgentAccess(c)

	require.Empty(t, c.Errors.Errors())
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "ali", members.queried, "the search term must reach the member query")

	var resp struct {
		Data agentAccessResponse `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
	assert.Equal(t, "allowlist", resp.Data.VisibilityMode)
	assert.EqualValues(t, 2, resp.Data.GrantedTotal, "grants on other pages still count")
	assert.EqualValues(t, 4, resp.Data.Total)

	byUser := map[string]agentAccessMemberResponse{}
	for _, member := range resp.Data.Members {
		byUser[member.UserID] = member
	}
	assert.True(t, byUser["creator-1"].Implicit, "the creator always sees the agent")
	assert.True(t, byUser["admin-1"].Implicit, "workspace admins always see the agent")
	assert.False(t, byUser["granted-1"].Implicit)
	assert.True(t, byUser["granted-1"].CanView)
	assert.Equal(t, "Alice", byUser["granted-1"].Username)
	assert.Equal(t, "alice@example.com", byUser["granted-1"].Email)
	assert.Equal(t, "suspended", byUser["other-1"].Status, "an inactive grant must stay visible so it can be revoked")
}

// A shared agent belongs to another workspace and its allowlist deliberately
// does not apply; the policy is not the borrowing workspace's to read.
func TestGetAgentAccessRejectsForeignWorkspace(t *testing.T) {
	h, _, _, agents := agentAccessFixture()
	agents.agent = &types.CustomAgent{ID: "agent-1", TenantID: 999, CreatedBy: "elsewhere"}
	c, _ := agentAccessContext(http.MethodGet, "/api/v1/agents/agent-1/access", nil,
		gin.Params{{Key: "id", Value: "agent-1"}})
	h.GetAgentAccess(c)

	require.Len(t, c.Errors, 1)
	appErr, ok := c.Errors[0].Err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, apperrors.ErrForbidden, appErr.Code)
}

// PATCH must be incremental: the panel only sends the rows the operator
// touched, and a page-sized replace would revoke everyone else.
func TestPatchAgentAccessSendsOnlyTouchedRows(t *testing.T) {
	h, grants, _, _ := agentAccessFixture()
	body, err := json.Marshal(agentAccessPayload{
		VisibilityMode: "allowlist",
		Members: []agentAccessMemberPayload{
			{UserID: "granted-1", CanView: false},
			{UserID: "other-1", CanView: true},
			{UserID: "creator-1", CanView: true}, // implicit: dropped
		},
	})
	require.NoError(t, err)

	c, w := agentAccessContext(http.MethodPatch, "/api/v1/agents/agent-1/access", body,
		gin.Params{{Key: "id", Value: "agent-1"}})
	h.PatchAgentAccess(c)

	require.Empty(t, c.Errors.Errors())
	require.Equal(t, http.StatusOK, w.Code)
	assert.True(t, grants.patchCalled)
	assert.Empty(t, grants.replaced, "PATCH must never fall back to a full replace")
	assert.Equal(t, []types.AgentUserGrant{
		{UserID: "granted-1", CanView: false},
		{UserID: "other-1", CanView: true},
	}, grants.patch)
	assert.Equal(t, types.AgentVisibilityAllowlist, grants.savedMode)
}

// An unknown mode must be rejected instead of being normalized to tenant: a
// typo would silently widen access to the whole workspace.
func TestPatchAgentAccessRejectsUnknownMode(t *testing.T) {
	h, grants, _, _ := agentAccessFixture()
	body, err := json.Marshal(agentAccessPayload{VisibilityMode: "tenant-wide"})
	require.NoError(t, err)

	c, _ := agentAccessContext(http.MethodPatch, "/api/v1/agents/agent-1/access", body,
		gin.Params{{Key: "id", Value: "agent-1"}})
	h.PatchAgentAccess(c)

	require.Len(t, c.Errors, 1)
	appErr, ok := c.Errors[0].Err.(*apperrors.AppError)
	require.True(t, ok)
	assert.Equal(t, apperrors.ErrBadRequest, appErr.Code)
	assert.Empty(t, grants.savedMode)
}

func TestAgentAccessImplicitMirrorsDecision(t *testing.T) {
	agent := &types.CustomAgent{ID: "a", CreatedBy: "u-creator"}
	assert.True(t, agentAccessImplicit(agent, &types.TenantMember{UserID: "u-creator", Role: types.TenantRoleViewer}))
	assert.True(t, agentAccessImplicit(agent, &types.TenantMember{UserID: "u-owner", Role: types.TenantRoleOwner}))
	assert.True(t, agentAccessImplicit(agent, &types.TenantMember{UserID: "u-admin", Role: types.TenantRoleAdmin}))
	assert.False(t, agentAccessImplicit(agent, &types.TenantMember{UserID: "u-viewer", Role: types.TenantRoleViewer}))
}
