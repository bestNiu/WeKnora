package wecomauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthorizeURLUsesSilentScopeAndKeepsFragment(t *testing.T) {
	client := New(Options{CorpID: "wwcorp", AgentID: 1000131, Secret: "s"})

	got := client.AuthorizeURL("https://example.com/api/v1/auth/wecom/callback", "state-1")

	require.True(t, strings.HasPrefix(got, DefaultAuthorizeBaseURL+"/connect/oauth2/authorize?"),
		"unexpected authorize origin: %s", got)
	require.Contains(t, got, "appid=wwcorp")
	require.Contains(t, got, "agentid=1000131")
	require.Contains(t, got, "scope=snsapi_base")
	require.Contains(t, got, "state=state-1")
	require.Contains(t, got, "redirect_uri=https%3A%2F%2Fexample.com%2Fapi%2Fv1%2Fauth%2Fwecom%2Fcallback")
	// WeCom requires the fragment to be the final element.
	require.True(t, strings.HasSuffix(got, "#wechat_redirect"), "fragment missing: %s", got)
}

func TestAccessTokenIsCached(t *testing.T) {
	var tokenCalls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/cgi-bin/gettoken", r.URL.Path)
		require.Equal(t, "wwcorp", r.URL.Query().Get("corpid"))
		mu.Lock()
		tokenCalls++
		mu.Unlock()
		fmt.Fprint(w, `{"errcode":0,"errmsg":"ok","access_token":"tok-1","expires_in":7200}`)
	}))
	defer server.Close()

	client := New(Options{CorpID: "wwcorp", Secret: "s", APIBaseURL: server.URL})

	for i := 0; i < 3; i++ {
		token, err := client.AccessToken(context.Background())
		require.NoError(t, err)
		require.Equal(t, "tok-1", token)
	}
	require.Equal(t, 1, tokenCalls, "a cached token must not hit gettoken again")
}

func TestAccessTokenErrorIsTyped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"errcode":40013,"errmsg":"invalid corpid"}`)
	}))
	defer server.Close()

	client := New(Options{CorpID: "bad", Secret: "s", APIBaseURL: server.URL})

	_, err := client.AccessToken(context.Background())
	require.Error(t, err)
	var apiErr *Error
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, 40013, apiErr.Code)
}

func TestUserIDByCodeReturnsMemberUserID(t *testing.T) {
	server := wecomTestServer(t, map[string]string{
		"/cgi-bin/auth/getuserinfo": `{"errcode":0,"errmsg":"ok","userid":"zhangsan"}`,
	})
	defer server.Close()

	client := New(Options{CorpID: "wwcorp", Secret: "s", APIBaseURL: server.URL})

	userid, err := client.UserIDByCode(context.Background(), "code-1")
	require.NoError(t, err)
	require.Equal(t, "zhangsan", userid)
}

func TestUserIDByCodeRejectsNonMember(t *testing.T) {
	// A non-member authorization answers with openid instead of userid; the
	// login flow must refuse rather than provision a guest account.
	server := wecomTestServer(t, map[string]string{
		"/cgi-bin/auth/getuserinfo": `{"errcode":0,"errmsg":"ok","openid":"o-123"}`,
	})
	defer server.Close()

	client := New(Options{CorpID: "wwcorp", Secret: "s", APIBaseURL: server.URL})

	_, err := client.UserIDByCode(context.Background(), "code-1")
	require.ErrorIs(t, err, ErrNonMember)
	require.True(t, wecomNonMember(t, err))
}

func TestMemberReturnsProfile(t *testing.T) {
	server := wecomTestServer(t, map[string]string{
		"/cgi-bin/user/get": `{"errcode":0,"errmsg":"ok","userid":"zhangsan","name":"张三","email":"zs@example.com"}`,
	})
	defer server.Close()

	client := New(Options{CorpID: "wwcorp", Secret: "s", APIBaseURL: server.URL})

	member, err := client.Member(context.Background(), "zhangsan")
	require.NoError(t, err)
	require.Equal(t, "张三", member.Name)
	require.Equal(t, "zs@example.com", member.Email)
}

func TestUserIDByCodeRequiresCode(t *testing.T) {
	client := New(Options{CorpID: "wwcorp", Secret: "s"})
	_, err := client.UserIDByCode(context.Background(), "  ")
	require.Error(t, err)
}

// wecomTestServer serves a token for /cgi-bin/gettoken plus the given responses
// keyed by path.
func wecomTestServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cgi-bin/gettoken" {
			fmt.Fprint(w, `{"errcode":0,"errmsg":"ok","access_token":"tok","expires_in":7200}`)
			return
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected wecom path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		require.Equal(t, "tok", r.URL.Query().Get("access_token"))
		fmt.Fprint(w, body)
	}))
}

func wecomNonMember(t *testing.T, err error) bool {
	t.Helper()
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return IsNonMember(err)
	}
	return errors.Is(err, ErrNonMember)
}