package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kbAccessKBStub struct {
	interfaces.KnowledgeBaseService
	kb   *types.KnowledgeBase
	mode types.KBVisibilityMode
}

func (s *kbAccessKBStub) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func (s *kbAccessKBStub) SetVisibilityMode(_ context.Context, _ string, mode types.KBVisibilityMode) error {
	s.mode = mode
	return nil
}

type kbAccessGrantStub struct {
	interfaces.KBUserAccessService
	rows     []*types.KBUserAccess
	replaced []types.KBUserGrant
	decision types.KBAccessDecision
}

func (s *kbAccessGrantStub) ListByKB(context.Context, uint64, string) ([]*types.KBUserAccess, error) {
	return s.rows, nil
}

func (s *kbAccessGrantStub) ReplaceForKB(
	_ context.Context, _ uint64, _ string, _ string, grants []types.KBUserGrant,
) error {
	s.replaced = grants
	return nil
}

func (s *kbAccessGrantStub) Decide(
	context.Context, *types.KnowledgeBase, string, types.TenantRole, bool,
) (types.KBAccessDecision, error) {
	return s.decision, nil
}

func runKBAccess(
	t *testing.T,
	method string,
	path string,
	payload any,
	kb *types.KnowledgeBase,
	grants *kbAccessGrantStub,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := types.WithCaller(c.Request.Context(), types.Caller{
			TenantID: 7, UserID: "user-1", Role: types.TenantRoleAdmin,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	h := &KBAccessAdminHandler{
		kbService:     &kbAccessKBStub{kb: kb},
		accessService: grants,
	}
	router.GET("/knowledge-bases/:id/access", h.GetKBAccess)
	router.PUT("/knowledge-bases/:id/access", h.PutKBAccess)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestGetKBAccessReturnsPolicyAndCallerPermission(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, CreatorID: "creator", VisibilityMode: "restricted"}
	grants := &kbAccessGrantStub{
		rows:     []*types.KBUserAccess{{UserID: "u1", CanView: true, CanDownload: true}},
		decision: types.KBAccessDecision{CanView: true, CanDownload: true},
	}
	w := runKBAccess(t, http.MethodGet, "/knowledge-bases/kb-1/access", nil, kb, grants)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"visibility_mode":"restricted"`)
	assert.Contains(t, w.Body.String(), `"can_download":true`)
}

func TestPutKBAccessDropsCreatorAndNormalisesGrants(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "kb-1", TenantID: 7, CreatorID: "creator", VisibilityMode: "tenant"}
	grants := &kbAccessGrantStub{}
	payload := kbAccessPayload{
		VisibilityMode: "restricted",
		Members: []kbAccessMemberPayload{
			{UserID: "creator", CanView: true},
			{UserID: "u1", CanDownload: true},
			{UserID: "u2"},
		},
	}
	w := runKBAccess(t, http.MethodPut, "/knowledge-bases/kb-1/access", payload, kb, grants)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, grants.replaced, 3, "restricted persists the creator alongside the requested members")
	byUser := map[string]types.KBUserGrant{}
	for _, grant := range grants.replaced {
		byUser[grant.UserID] = grant
	}
	assert.True(t, byUser["u1"].CanDownload)
	assert.False(t, byUser["u1"].CanView, "view is derived from download by the repository normaliser")
	assert.False(t, byUser["u2"].CanView, "an all-off grant is forwarded; the repo turns it into a revoke")
	assert.True(t, byUser["creator"].CanView, "the creator always keeps access to a restricted KB")
	assert.True(t, byUser["creator"].CanDownload)
	assert.Contains(t, w.Body.String(), `"visibility_mode":"restricted"`)
}
