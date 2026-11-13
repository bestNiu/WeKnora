package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/Tencent/WeKnora/internal/wecomauth"
)

// WeCom (企业微信) web login. The flow mirrors the OIDC one so the frontend keeps
// a single "hash payload on the login page" contract:
//
//	GET /auth/wecom/authorize?redirect=/  → 302 to open.weixin.qq.com
//	GET /auth/wecom/callback?code&state    → 302 to <redirect>#wecom_result=<b64>
//
// The state is signed and nonce-bound with the same helper the OIDC callback
// uses, so a code cannot be replayed into another browser.

var (
	wecomClientOnce sync.Once
	wecomClient     *wecomauth.Client
)

// wecomAuthClient lazily builds the shared client from config. Lazy wiring keeps
// the constructor signature (and every existing test fixture) unchanged.
func (h *AuthHandler) wecomAuthClient() *wecomauth.Client {
	cfg := h.configInfo
	if cfg == nil || !cfg.WeComAuth.Enabled() {
		return nil
	}
	wecomClientOnce.Do(func() {
		wecomClient = wecomauth.New(wecomauth.Options{
			CorpID:  cfg.WeComAuth.CorpID,
			AgentID: cfg.WeComAuth.AgentID,
			Secret:  cfg.WeComAuth.AgentSecret,
		})
	})
	return wecomClient
}

// GetWeComConfig reports whether the WeCom login entry should render.
func (h *AuthHandler) GetWeComConfig(c *gin.Context) {
	enabled := h.wecomAuthClient() != nil
	displayName := ""
	if h.configInfo != nil && h.configInfo.WeComAuth != nil {
		displayName = h.configInfo.WeComAuth.DisplayName
	}
	c.JSON(http.StatusOK, gin.H{
		"success":      true,
		"enabled":      enabled,
		"display_name": displayName,
	})
}

// safeFrontendRedirect keeps post-login navigation on this origin. An absolute
// or protocol-relative target would turn the callback into an open redirect.
func safeFrontendRedirect(raw string) string {
	target := strings.TrimSpace(raw)
	if target == "" || !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return "/"
	}
	return target
}

// WeComAuthorize starts the silent authorization: 302 straight to WeCom, no
// client-side JS needed. `redirect` is where the login page lands afterwards.
func (h *AuthHandler) WeComAuthorize(c *gin.Context) {
	client := h.wecomAuthClient()
	if client == nil {
		c.Error(errors.NewForbiddenError("企业微信登录未启用"))
		return
	}

	nonce, err := randomNonce()
	if err != nil {
		c.Error(errors.NewInternalServerError("生成登录状态失败"))
		return
	}
	state, err := signAuthState(nonce, safeFrontendRedirect(c.Query("redirect")))
	if err != nil {
		c.Error(errors.NewInternalServerError("生成登录状态失败"))
		return
	}
	// Same nonce↔browser binding the OIDC flow uses; one cookie name keeps the
	// callback verification logic single-implementation.
	setOIDCNonceCookie(c, nonce)

	c.Redirect(http.StatusFound, client.AuthorizeURL(h.wecomCallbackURL(c), state))
}

// wecomCallbackURL is the URL WeCom redirects back to. Config may pin it (when
// the public origin differs from the forwarded host); otherwise it is derived
// from the request.
func (h *AuthHandler) wecomCallbackURL(c *gin.Context) string {
	if h.configInfo != nil && h.configInfo.WeComAuth != nil {
		if configured := strings.TrimSpace(h.configInfo.WeComAuth.RedirectURI); configured != "" {
			return configured
		}
	}
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/api/v1/auth/wecom/callback"
}

// WeComCallback exchanges the code for a member userid and completes login.
// Errors are reported through the URL hash (like the OIDC callback) so the
// frontend owns all messaging.
func (h *AuthHandler) WeComCallback(c *gin.Context) {
	client := h.wecomAuthClient()
	if client == nil {
		c.Error(errors.NewForbiddenError("企业微信登录未启用"))
		return
	}

	ctx := c.Request.Context()

	decodedState, err := decodeOIDCState(strings.TrimSpace(c.Query("state")), c.Request)
	if err != nil {
		logger.Errorf(ctx, "WeCom callback: invalid state: %v", err)
		c.Redirect(http.StatusFound, "/#wecom_error="+urlQueryEscape("invalid_state"))
		return
	}
	target := safeFrontendRedirect(decodedState.RedirectURI)
	// One-time use: clear the binding cookie as soon as it is checked.
	c.SetCookie(oidcNonceCookieName, "", -1, "/", "", false, true)

	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		c.Redirect(http.StatusFound, target+"#oidc_error="+urlQueryEscape("missing_code"))
		return
	}

	userid, err := client.UserIDByCode(ctx, code)
	if err != nil {
		reason := "code_exchange_failed"
		if wecomauthIsNonMember(err) {
			reason = "not_member"
		}
		logger.Errorf(ctx, "WeCom callback: exchange failed: %v", err)
		c.Redirect(http.StatusFound, target+"#oidc_error="+urlQueryEscape(reason))
		return
	}

	opts := h.wecomLoginOptions(ctx)
	// Best-effort directory read: it supplies the display name and mailbox, and
	// a mailbox that matches an existing account merges the member into it.
	// Failure is expected without the contacts permission and only costs a
	// readable username.
	if member, memberErr := client.Member(ctx, userid); memberErr == nil && member != nil {
		opts.DisplayName = member.Name
		opts.Email = member.Email
	} else if memberErr != nil {
		logger.Debugf(ctx, "WeCom callback: member profile unavailable for userid=%s: %v",
			wecomLogSafe(userid), memberErr)
	}

	resp, err := h.userService.LoginWithWeCom(ctx, userid, opts)
	if err != nil {
		logger.Errorf(ctx, "WeCom callback: login failed for userid=%s: %v",
			wecomLogSafe(userid), err)
		c.Redirect(http.StatusFound, target+"#oidc_error="+urlQueryEscape("login_failed"))
		return
	}
	if !resp.Success {
		// 首次接通过程中，管理员需要知道「谁的 userid 还没绑定」，而这只能在
		// 服务端日志里拿到（登录页不暴露成员账号）。绑定后即无输出。
		logger.Infof(ctx, "WeCom callback: userid=%s is not bound to any local account",
			wecomLogSafe(userid))
		c.Redirect(http.StatusFound, target+"#oidc_error="+urlQueryEscape("unbound")+
			"&oidc_error_description="+urlQueryEscape(resp.Message))
		return
	}

	// The result hash keys are the OIDC ones on purpose: the frontend already
	// has one global hash handler for #oidc_result/#oidc_error, and a second
	// contract would duplicate it for no behavioural gain.
	payload, err := encodeOIDCCallbackPayload(resp)
	if err != nil {
		logger.Errorf(ctx, "WeCom callback: payload encode failed: %v", err)
		c.Redirect(http.StatusFound, target+"#oidc_error="+urlQueryEscape("payload_encode_failed"))
		return
	}
	c.Redirect(http.StatusFound, target+"#oidc_result="+urlQueryEscape(payload))
}

// wecomLoginOptions projects config into the service-level policy. Always
// server-derived: a client can never widen access by asking for it.
func (h *AuthHandler) wecomLoginOptions(ctx context.Context) types.WeComLoginOptions {
	opts := types.WeComLoginOptions{Provisioning: h.resolveDefaultTenantMode(ctx)}
	cfg := h.configInfo
	if cfg == nil || cfg.WeComAuth == nil {
		return opts
	}
	opts.AutoProvision = cfg.WeComAuth.AutoProvision
	opts.JoinTenantID = cfg.WeComAuth.JoinTenantID
	opts.Role = types.TenantRole(strings.ToLower(strings.TrimSpace(cfg.WeComAuth.DefaultRole)))
	return opts
}

// wecomauthIsNonMember keeps the wecomauth import out of the error branch above
// readable; it also documents that a non-member must never be provisioned.
func wecomauthIsNonMember(err error) bool {
	return wecomauth.IsNonMember(err) || err == wecomauth.ErrNonMember
}

// randomNonce returns a URL-safe random string for the signed state.
func randomNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// signAuthState reuses the signed-state helper the OIDC callback verifies, so
// both flows share one HMAC key, one TTL, and one nonce-cookie contract.
func signAuthState(nonce, redirectURI string) (string, error) {
	return secutils.SignOIDCState(&secutils.OIDCStatePayload{
		Nonce:       nonce,
		RedirectURI: redirectURI,
	})
}

// wecomLogSafe strips characters that could forge log lines from a userid that
// originates outside our trust boundary.
func wecomLogSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
}
