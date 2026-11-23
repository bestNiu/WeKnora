package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

// /agents/usage is a static sibling of /agents/:id, and gin's radix tree is
// where that class of route silently breaks (a panic at startup, or /usage
// being swallowed by :id). Pin both: the group registers without panicking and
// each request lands on its own handler.
func TestAgentRoutesKeepUsageAndIDApart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	v1 := engine.Group("/api/v1")
	g := &rbacGuards{}
	noop := func(c *gin.Context) { c.String(http.StatusOK, "list") }
	usage := func(c *gin.Context) { c.String(http.StatusOK, "usage") }
	agent := func(c *gin.Context) { c.String(http.StatusOK, "agent:"+c.Param("id")) }

	agents := g.apiKeyGroup(v1.Group("/agents"), apiKeyFullAccess())
	agents.GET("", noop)
	agents.GET("/usage", usage)
	agents.GET("/:id", agent)

	for target, want := range map[string]string{
		"/api/v1/agents":       "list",
		"/api/v1/agents/usage": "usage",
		"/api/v1/agents/abc":   "agent:abc",
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusOK || w.Body.String() != want {
			t.Errorf("%s -> %d %q, want %q", target, w.Code, w.Body.String(), want)
		}
	}
}

// The handler identity matters here: a nil usage handler must skip the route
// instead of registering a panicking nil method value.
func TestRegisterCustomAgentRoutesToleratesNilUsageHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	v1 := engine.Group("/api/v1")
	g := &rbacGuards{}
	RegisterCustomAgentRoutes(v1, &handler.CustomAgentHandler{}, nil, nil, g)
}