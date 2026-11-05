package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

// TestApplyAuthSessionSetsBothSurfaces locks the core invariant of the
// helper: every attached value must be readable from BOTH c.Keys (c.Get)
// and the request context (types.*FromContext). A key present on only one
// surface is the class of bug the helper exists to prevent.
func TestApplyAuthSessionSetsBothSurfaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)

	user := &types.User{ID: "u1", IsSystemAdmin: true}
	tenant := &types.Tenant{ID: 7}
	principal := types.Principal{Type: types.PrincipalWebUser, ID: "u1"}
	// Bound to u1 so the caller subject matches the authenticated user this
	// fixture asserts on both surfaces.
	scope := &types.TenantAPIKeyScope{KeyID: 3, OwnerUserID: "u1"}
	applyAuthSession(c, authSession{
		User:        user,
		Principal:   principal,
		TenantID:    7,
		Tenant:      tenant,
		Role:        types.TenantRoleAdmin,
		SystemAdmin: true,
		APIKeyScope: scope,
		Extra:       map[types.ContextKey]any{types.EmbedChannelContextKey: &types.EmbedChannel{ID: "ch"}},
	})

	ctx := c.Request.Context()
	caller := types.Caller{TenantID: 7, UserID: "u1", Role: types.TenantRoleAdmin}
	if got := types.CallerFromContext(types.WithExecutionTenant(ctx, 9)); got != caller {
		t.Fatalf("execution switch changed authenticated caller: %+v", got)
	}
	if got, ok := types.TenantIDFromContext(ctx); !ok || got != 7 {
		t.Fatalf("ctx tenant id = %d, ok=%v", got, ok)
	}
	if got, ok := c.Get(types.TenantIDContextKey.String()); !ok || got.(uint64) != 7 {
		t.Fatalf("keys tenant id = %v, ok=%v", got, ok)
	}
	if got, ok := types.TenantInfoFromContext(ctx); !ok || got.ID != 7 {
		t.Fatalf("ctx tenant info = %#v, ok=%v", got, ok)
	}
	if got, ok := types.UserIDFromContext(ctx); !ok || got != "u1" {
		t.Fatalf("ctx user id = %q, ok=%v", got, ok)
	}
	if got, ok := c.Get(types.UserContextKey.String()); !ok || got.(*types.User).ID != "u1" {
		t.Fatalf("keys user = %#v, ok=%v", got, ok)
	}
	if got, ok := types.PrincipalFromContext(ctx); !ok || got != principal {
		t.Fatalf("ctx principal = %#v, ok=%v", got, ok)
	}
	if got := types.TenantRoleFromContext(ctx); got != types.TenantRoleAdmin {
		t.Fatalf("ctx role = %q", got)
	}
	if got, ok := c.Get(types.TenantRoleContextKey.String()); !ok || got.(types.TenantRole) != types.TenantRoleAdmin {
		t.Fatalf("keys role = %v, ok=%v", got, ok)
	}
	if !types.IsSystemAdminFromContext(ctx) {
		t.Fatal("ctx system admin flag lost")
	}
	if got, ok := types.TenantAPIKeyScopeFromContext(ctx); !ok || got.KeyID != 3 {
		t.Fatalf("ctx api key scope = %#v, ok=%v", got, ok)
	}
	if ch, ok := EmbedChannelFromContext(ctx); !ok || ch.ID != "ch" {
		t.Fatalf("ctx embed channel = %#v, ok=%v", ch, ok)
	}
	if got, ok := c.Get(types.EmbedChannelContextKey.String()); !ok || got.(*types.EmbedChannel).ID != "ch" {
		t.Fatalf("keys embed channel = %v, ok=%v", got, ok)
	}
}

// TestApplyAuthSessionTenantless verifies that a tenantless session attaches
// neither tenant keys nor a role key — RequireRole's fail-closed Viewer
// default depends on the role key being absent, and TENANT_REQUIRED
// handling depends on the tenant key being absent.
func TestApplyAuthSessionTenantless(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)

	attachTenantlessUserContext(c, &types.User{ID: "u2"})

	ctx := c.Request.Context()
	if _, ok := types.TenantIDFromContext(ctx); ok {
		t.Fatal("tenantless session must not carry a tenant id")
	}
	if _, ok := c.Get(types.TenantRoleContextKey.String()); ok {
		t.Fatal("tenantless session must not carry a role key")
	}
	if got, ok := types.UserIDFromContext(ctx); !ok || got != "u2" {
		t.Fatalf("ctx user id = %q, ok=%v", got, ok)
	}
	if types.IsSystemAdminFromContext(ctx) {
		t.Fatal("non-admin user must not be flagged system admin")
	}
}

func TestBearerToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		header string
		want   string
		ok     bool
	}{
		{"", "", false},
		{"Basic abc", "", false},
		{"Bearer", "", false},
		{"Bearer abc", "abc", true},
	}
	for _, tc := range cases {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		if tc.header != "" {
			c.Request.Header.Set("Authorization", tc.header)
		}
		got, ok := bearerToken(c)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("bearerToken(%q) = (%q, %v), want (%q, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

// A machine principal carries its bound owner as the effective caller user, so
// per-user KB access (visibility/download) applies to API keys and channel bots
// exactly as it does to a signed-in member. Unbound principals keep their
// synthetic identity and therefore only reach tenant-wide knowledge bases.
func TestApplyAuthSessionUsesBoundOwnerForMachinePrincipals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	user := &types.User{ID: "system-7"}

	c1, _ := gin.CreateTestContext(httptest.NewRecorder())
	c1.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	applyAuthSession(c1, authSession{
		User: user, TenantID: 7, Role: types.TenantRoleViewer,
		APIKeyScope: &types.TenantAPIKeyScope{KeyID: 3, OwnerUserID: "u-owner"},
	})
	if got := types.CallerFromContext(c1.Request.Context()).UserID; got != "u-owner" {
		t.Fatalf("bound owner not used as caller user: %q", got)
	}

	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	applyAuthSession(c2, authSession{
		User: user, TenantID: 7, Role: types.TenantRoleViewer,
		APIKeyScope: &types.TenantAPIKeyScope{KeyID: 4},
	})
	if got := types.CallerFromContext(c2.Request.Context()).UserID; got != "" {
		t.Fatalf("unbound machine principal must have no ACL subject, got %q", got)
	}
}

// The API-key session must carry the machine scope into the request context so
// the per-user ACL can recognise a machine principal and ignore its synthetic
// Owner role. Without that, a full-access key would bypass a restricted KB.
func TestAPIKeySessionCarriesScopeIntoACL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases/kb", nil)
	applyAuthSession(c, authSession{
		User:     &types.User{ID: "system-7"},
		TenantID: 7,
		Role:     types.TenantRoleOwner,
		APIKeyScope: &types.TenantAPIKeyScope{
			KeyID: 1, FullAccess: true, ScopeType: types.APIKeyScopeTenant,
		},
	})
	if _, ok := types.TenantAPIKeyScopeFromContext(c.Request.Context()); !ok {
		t.Fatal("API key scope missing from the request context")
	}
	restricted := &types.KnowledgeBase{ID: "kb", TenantID: 7, VisibilityMode: "restricted"}
	decision, err := access.DecideKBVisibility(c.Request.Context(), restricted, types.CallerFromContext(c.Request.Context()))
	if err != nil {
		t.Fatal(err)
	}
	if decision.CanView || decision.CanDownload {
		t.Fatalf("unbound full-access key bypassed the restricted KB: %+v", decision)
	}
}
