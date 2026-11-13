// Package wecomauth implements the WeCom (企业微信) self-built-application web
// login. WeCom's web authorization is NOT OpenID Connect: the browser returns a
// `code`, and the server exchanges it for a MEMBER USERID via
// /cgi-bin/auth/getuserinfo. That is why this adapter exists next to, rather
// than inside, the OIDC support.
//
// Reference: https://developer.work.weixin.qq.com/document/path/91022
package wecomauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultAPIBaseURL is the WeCom server API origin.
	DefaultAPIBaseURL = "https://qyapi.weixin.qq.com"
	// DefaultAuthorizeBaseURL is the OAuth2 consent/silent-authorization origin.
	DefaultAuthorizeBaseURL = "https://open.weixin.qq.com"

	// tokenRefreshMargin refreshes the cached access_token before it actually
	// expires. WeCom tokens are valid for 7200s; a shared token that dies
	// mid-request turns every concurrent login into a 42001 error.
	tokenRefreshMargin = 5 * time.Minute
)

// Error is a WeCom API error carrying the numeric errcode.
type Error struct {
	Code int    `json:"errcode"`
	Msg  string `json:"errmsg"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("wecom api error %d: %s", e.Code, e.Msg)
}

// IsNonMember reports whether the code belonged to someone outside the
// enterprise. getuserinfo answers with an openid instead of a userid then, and
// such a caller must never be provisioned a local account.
func IsNonMember(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == 40029 || apiErr.Code == 40031
}

// ErrNonMember is returned when the authorized WeCom user is not a member of
// this enterprise (getuserinfo yields openid, not userid).
var ErrNonMember = errors.New("wecom user is not a member of this enterprise")

// Member is the subset of /cgi-bin/user/get the login flow uses.
type Member struct {
	UserID string `json:"userid"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Mobile string `json:"mobile"`
	Avatar string `json:"avatar"`
}

// Client is a concurrency-safe WeCom API client with a shared token cache.
type Client struct {
	corpID     string
	agentID    int
	secret     string
	apiBase    string
	authorizeBase string
	httpClient *http.Client

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// Options configures a Client. Empty URLs fall back to the production origins,
// which keeps test wiring explicit without a second constructor.
type Options struct {
	CorpID        string
	AgentID       int
	Secret        string
	APIBaseURL    string
	AuthorizeBaseURL string
	HTTPClient    *http.Client
}

// New builds a Client.
func New(opts Options) *Client {
	apiBase := strings.TrimSpace(opts.APIBaseURL)
	if apiBase == "" {
		apiBase = DefaultAPIBaseURL
	}
	authorizeBase := strings.TrimSpace(opts.AuthorizeBaseURL)
	if authorizeBase == "" {
		authorizeBase = DefaultAuthorizeBaseURL
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		corpID:        strings.TrimSpace(opts.CorpID),
		agentID:       opts.AgentID,
		secret:        strings.TrimSpace(opts.Secret),
		apiBase:       strings.TrimRight(apiBase, "/"),
		authorizeBase: strings.TrimRight(authorizeBase, "/"),
		httpClient:    httpClient,
	}
}

// AuthorizeURL builds the browser-facing authorization URL. scope=snsapi_base
// yields a silent redirect (no consent screen) and only identifies the member;
// snsapi_privateinfo would ask for consent to read profile fields, which the
// login flow does not need.
func (c *Client) AuthorizeURL(redirectURI, state string) string {
	query := url.Values{}
	query.Set("appid", c.corpID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", "snsapi_base")
	query.Set("state", state)
	if c.agentID != 0 {
		query.Set("agentid", fmt.Sprintf("%d", c.agentID))
	}
	// The #wechat_redirect fragment is required by WeCom and must stay last.
	return c.authorizeBase + "/connect/oauth2/authorize?" + query.Encode() + "#wechat_redirect"
}

// AccessToken returns a cached token, fetching a new one when the cache is
// cold or inside the refresh margin. The mutex is held across the fetch so
// concurrent logins do not race to mint tokens and invalidate each other.
func (c *Client) AccessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expiresAt.Add(-tokenRefreshMargin)) {
		return c.token, nil
	}

	var resp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	endpoint := c.apiBase + "/cgi-bin/gettoken?" + url.Values{
		"corpid":     {c.corpID},
		"corpsecret": {c.secret},
	}.Encode()
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errors.New("wecom gettoken returned an empty access_token")
	}

	c.token = resp.AccessToken
	expiresIn := resp.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	c.expiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	return c.token, nil
}

// UserIDByCode exchanges a web-authorization code for the member userid.
// A non-member caller gets ErrNonMember instead of an openid, so the caller
// can never provision a guest account by accident.
func (c *Client) UserIDByCode(ctx context.Context, code string) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", errors.New("wecom authorization code is required")
	}
	token, err := c.AccessToken(ctx)
	if err != nil {
		return "", err
	}

	var resp struct {
		UserID     string `json:"userid"`
		OpenID     string `json:"openid"`
		UserTicket string `json:"user_ticket"`
	}
	endpoint := c.apiBase + "/cgi-bin/auth/getuserinfo?" + url.Values{
		"access_token": {token},
		"code":         {code},
	}.Encode()
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.UserID) == "" {
		// openid means "authorized, but not a member of this enterprise".
		return "", ErrNonMember
	}
	return resp.UserID, nil
}

// Member loads a member's display fields. The login flow treats a failure here
// as non-fatal: the userid alone is enough to authenticate.
func (c *Client) Member(ctx context.Context, userid string) (*Member, error) {
	userid = strings.TrimSpace(userid)
	if userid == "" {
		return nil, errors.New("wecom userid is required")
	}
	token, err := c.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	var resp Member
	endpoint := c.apiBase + "/cgi-bin/user/get?" + url.Values{
		"access_token": {token},
		"userid":       {userid},
	}.Encode()
	if err := c.getJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// getJSON performs a GET and decodes a JSON body, mapping a non-zero errcode
// onto *Error so callers can branch on the numeric code.
func (c *Client) getJSON(ctx context.Context, endpoint string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build wecom request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call wecom api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read wecom response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wecom api http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	// Every WeCom response carries errcode/errmsg alongside the payload, so
	// the envelope is decoded first and the payload only on success.
	var envelope Error
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode wecom response: %w", err)
	}
	if envelope.Code != 0 {
		return &envelope
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode wecom response: %w", err)
	}
	return nil
}