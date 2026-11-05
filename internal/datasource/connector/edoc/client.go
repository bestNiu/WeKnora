package edoc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
)

const (
	userAgent       = "WeKnora-EDoc-Connector/1.0"
	apiTimeout      = 60 * time.Second
	downloadTimeout = 300 * time.Second

	// maxAttempts is the initial request plus two retries.
	maxAttempts = 3

	jsonLimit = int64(32 << 20)
	// filePageSize is the hard maximum eDoc accepts for one page.
	filePageSize = 100
	// maxPages bounds a pagination loop so a server that keeps returning a
	// full page cannot spin forever.
	maxPages = 10000
)

var (
	errTokenExpired   = errors.New("edoc token expired")
	errInvalidReply   = errors.New("edoc returned an unexpected response")
	errTooLarge       = errors.New("edoc file exceeds the size limit")
	errEmptyFile      = errors.New("edoc file is empty")
	errUnsafeEndpoint = errors.New("edoc endpoint was blocked by the SSRF policy")

	// sleep performs every backoff wait; tests replace it to avoid real waits.
	sleep = sleepCtx
)

// client talks to one eDoc deployment. The SDK endpoints take the session
// token as a query parameter; the token is obtained through integration login
// and refreshed once if the server reports it as expired.
type client struct {
	cfg config
	api *http.Client
	dl  *http.Client

	mu    sync.Mutex
	token string
}

func newClient(cfg config) *client {
	return &client{
		cfg: cfg,
		api: datasource.NewConnectorHTTPClient(apiTimeout),
		dl:  datasource.NewConnectorHTTPClient(downloadTimeout),
	}
}

// httpError reports a non-success status. Op is a fixed label, never a URL or
// response body, so the message is safe to surface.
type httpError struct {
	Op     string
	Status int
}

func (e *httpError) Error() string { return fmt.Sprintf("edoc %s: HTTP %d", e.Op, e.Status) }
func (e *httpError) Unwrap() error { return datasource.ErrFetchFailed }

// statusOf returns the HTTP status carried by err, or 0.
func statusOf(err error) int {
	var statusErr *httpError
	if errors.As(err, &statusErr) {
		return statusErr.Status
	}
	return 0
}

// transportError hides the request URL while remembering whether a retry is
// worthwhile.
type transportError struct {
	Op        string
	transient bool
}

func (e *transportError) Error() string { return "edoc " + e.Op + ": request failed" }
func (e *transportError) Unwrap() error { return datasource.ErrFetchFailed }

func invalidResponse(op string) error {
	return fmt.Errorf("%w: edoc %s: %w", datasource.ErrFetchFailed, op, errInvalidReply)
}

func isTransientNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func backoff(attempt int) time.Duration { return time.Second << attempt }

// login obtains a session token through integration login. It never returns a
// token derived from a stored password: the integration key is the only
// credential the connector holds.
func (c *client) login(ctx context.Context) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"loginName":      c.cfg.loginName,
		"ipAddress":      c.cfg.ipAddress,
		"integrationKey": c.cfg.integrationKey,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", datasource.ErrInvalidCredentials, err)
	}
	target := c.cfg.baseURL + "/api/services/org/UserLoginIntegrationByUserLoginName"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("%w: invalid base_url", datasource.ErrInvalidConfig)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.api.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: edoc login: request failed", datasource.ErrFetchFailed)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: edoc login: HTTP %d", datasource.ErrInvalidCredentials, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &httpError{Op: "login", Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, jsonLimit))
	if err != nil {
		return "", fmt.Errorf("%w: edoc login: request failed", datasource.ErrFetchFailed)
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", invalidResponse("login")
	}
	if env.ErrorCode != "" {
		return "", fmt.Errorf("%w: edoc login: %s", datasource.ErrInvalidCredentials, shortReason(env.ErrorMsg))
	}
	if env.Result != nil && *env.Result != 0 {
		return "", fmt.Errorf("%w: edoc login rejected (result %d) %s",
			datasource.ErrInvalidCredentials, *env.Result, shortReason(env.Message))
	}
	var token string
	if err := json.Unmarshal(env.Data, &token); err != nil || strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("%w: edoc login returned no token", datasource.ErrInvalidCredentials)
	}
	token = strings.TrimSpace(token)
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	return token, nil
}

// shortReason trims a server message for surfacing; eDoc can echo long
// exception text, which must not reach sync logs whole.
func shortReason(msg string) string {
	msg = strings.TrimSpace(msg)
	const maxLen = 160
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}

func (c *client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		return token, nil
	}
	return c.login(ctx)
}

// refreshToken replaces a token the server rejected. A concurrent caller may
// have refreshed already; the current value is then returned as-is.
func (c *client) refreshToken(ctx context.Context, stale string) (string, error) {
	c.mu.Lock()
	current := c.token
	c.mu.Unlock()
	if current != "" && current != stale {
		return current, nil
	}
	return c.login(ctx)
}

// do performs one endpoint call with retries on throttling, upstream 5xx and
// transient network errors, and refreshes the token once when eDoc reports it
// as expired. limit caps the response body.
func (c *client) do(
	ctx context.Context, endpoint string, query url.Values, op string, limit int64,
) ([]byte, error) {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	var refreshed bool
	for attempt := 0; ; attempt++ {
		body, err := c.once(ctx, endpoint, withToken(query, token), op, limit)
		if err == nil {
			return body, nil
		}
		if errors.Is(err, errTokenExpired) && !refreshed {
			refreshed = true
			token, err = c.refreshToken(ctx, token)
			if err != nil {
				return nil, err
			}
			attempt--
			continue
		}
		delay, retry := classify(err, attempt)
		if !retry || attempt >= maxAttempts-1 {
			return nil, err
		}
		if err := sleep(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// classify decides whether err is worth retrying and how long to wait.
func classify(err error, attempt int) (time.Duration, bool) {
	var statusErr *httpError
	var netErr *transportError
	switch {
	case errors.As(err, &statusErr):
		return backoff(attempt), retryableStatus(statusErr.Status)
	case errors.As(err, &netErr):
		return backoff(attempt), netErr.transient
	default:
		return 0, false
	}
}

func (c *client) once(
	ctx context.Context, endpoint string, query url.Values, op string, limit int64,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	token, err := c.ensureToken(ctx)
	if err != nil {
		return nil, err
	}
	query = withToken(query, token)
	target := c.cfg.baseURL + endpoint + "?" + query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, invalidResponse(op)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	httpClient := c.api
	if limit > jsonLimit {
		httpClient = c.dl
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &transportError{Op: op, transient: isTransientNetworkError(err)}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: %w", datasource.ErrFetchFailed, &httpError{Op: op, Status: resp.StatusCode})
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpError{Op: op, Status: resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &transportError{Op: op, transient: isTransientNetworkError(err)}
	}
	if int64(len(body)) > limit {
		return nil, errTooLarge
	}
	// eDoc answers an expired session with HTTP 200 and {"errorCode":"4"}.
	// Any other errorCode is a server-side failure, not an auth problem.
	if code, msg, ok := apiErrorCode(body); ok {
		if code == "4" || code == "ErrorCode4" {
			return nil, errTokenExpired
		}
		return nil, fmt.Errorf("%w: edoc %s: %s", datasource.ErrFetchFailed, op, shortReason(msg))
	}
	return body, nil
}

func withToken(query url.Values, token string) url.Values {
	out := url.Values{}
	for k, vs := range query {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	out.Set("token", token)
	return out
}

func tokenFrom(query url.Values) string { return query.Get("token") }

// apiErrorCode reports the errorCode/errorMsg pair eDoc returns instead of a
// typed result when a request itself fails.
func apiErrorCode(body []byte) (code, message string, ok bool) {
	var probe struct {
		ErrorCode string `json:"errorCode"`
		ErrorMsg  string `json:"errorMsg"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", "", false
	}
	if probe.ErrorCode == "" {
		return "", "", false
	}
	return probe.ErrorCode, probe.ErrorMsg, true
}

// getJSON performs a call returning the ReturnValueResult<T> envelope and
// decodes data into out (nil discards it).
func (c *client) getJSON(
	ctx context.Context, endpoint string, query url.Values, op string, out any,
) error {
	body, err := c.do(ctx, endpoint, query, op, jsonLimit)
	if err != nil {
		return err
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return invalidResponse(op)
	}
	if env.ErrorCode != "" {
		return fmt.Errorf("%w: edoc %s: %s", datasource.ErrFetchFailed, op, shortReason(env.ErrorMsg))
	}
	if env.Result == nil {
		return invalidResponse(op)
	}
	if *env.Result != 0 {
		return fmt.Errorf("%w: edoc %s: result %d %s",
			datasource.ErrFetchFailed, op, *env.Result, shortReason(env.Message))
	}
	if out == nil {
		return nil
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return invalidResponse(op)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return invalidResponse(op)
	}
	return nil
}

// topFolders returns the enterprise and personal library roots.
func (c *client) topFolders(ctx context.Context) (rootFolders, error) {
	var roots rootFolders
	err := c.getJSON(ctx, "/api/services/Doc/GetTopFolderId", nil, "top folders", &roots)
	return roots, err
}

// folderInfo returns one folder's metadata (name, parent, child counts).
func (c *client) folderInfo(ctx context.Context, id int64) (folderInfo, error) {
	var info folderInfo
	query := url.Values{"folderId": {strconv.FormatInt(id, 10)}}
	if err := c.getJSON(ctx, "/api/services/Folder/GetFolderInfoById", query, "folder info", &info); err != nil {
		return folderInfo{}, err
	}
	return info, nil
}

// listChildren returns the direct sub-folders of a folder.
func (c *client) listChildren(ctx context.Context, id int64) ([]folderInfo, error) {
	var children []folderInfo
	query := url.Values{"topFolderId": {strconv.FormatInt(id, 10)}}
	if err := c.getJSON(ctx, "/api/services/Folder/GetChildrenFolders", query, "folder children", &children); err != nil {
		return nil, err
	}
	return children, nil
}

// listFiles returns every file directly inside a folder, following the
// pageSize cap the endpoint enforces.
func (c *client) listFiles(ctx context.Context, folderID int64) ([]fileInfo, error) {
	out := make([]fileInfo, 0, filePageSize)
	for page := 1; page <= maxPages; page++ {
		query := url.Values{
			"folderId":  {strconv.FormatInt(folderID, 10)},
			"pageIndex": {strconv.Itoa(page)},
			"pageSize":  {strconv.Itoa(filePageSize)},
		}
		body, err := c.do(ctx, "/api/services/File/GetChildFilePageListByFolderId", query, "file list", jsonLimit)
		if err != nil {
			return nil, err
		}
		var paged filePage
		if err := json.Unmarshal(body, &paged); err != nil {
			return nil, invalidResponse("file list")
		}
		if paged.Result != 0 {
			return nil, fmt.Errorf("%w: edoc file list: result %d", datasource.ErrFetchFailed, paged.Result)
		}
		out = append(out, paged.Files...)
		if len(paged.Files) < filePageSize || len(out) >= paged.TotalCount {
			return out, nil
		}
	}
	return nil, fmt.Errorf("%w: edoc file list exceeded %d pages", datasource.ErrFetchFailed, maxPages)
}

// downloadCheck asks the download gate whether the current version of a file
// may be read and returns the region hash used by the download endpoint.
func (c *client) downloadCheck(ctx context.Context, fileID int64) (downloadCheck, error) {
	query := url.Values{"fileIds": {strconv.FormatInt(fileID, 10)}}
	body, err := c.do(ctx, "/DownLoad/DownLoadCheck", query, "download check", jsonLimit)
	if err != nil {
		return downloadCheck{}, err
	}
	var check downloadCheck
	if err := json.Unmarshal(body, &check); err != nil {
		return downloadCheck{}, invalidResponse("download check")
	}
	if check.NResult != 0 {
		return check, &downloadDenied{NResult: check.NResult, Msg: check.Msg}
	}
	if strings.TrimSpace(check.RegionHash) == "" {
		return check, invalidResponse("download check")
	}
	return check, nil
}

// downloadDenied is the gate's refusal: 5 = no permission, 601 = missing.
type downloadDenied struct {
	NResult int
	Msg     string
}

func (e *downloadDenied) Error() string {
	return fmt.Sprintf("edoc download denied: nResult %d %s", e.NResult, shortReason(e.Msg))
}

func (e *downloadDenied) Unwrap() error { return datasource.ErrFetchFailed }

// download streams the file body for a region hash, capped at limit bytes.
func (c *client) download(ctx context.Context, regionHash string, limit int64) ([]byte, error) {
	query := url.Values{"regionHash": {regionHash}, "async": {"false"}}
	body, err := c.do(ctx, "/downLoad/index", query, "download", limit+1)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errTooLarge
	}
	if len(body) == 0 {
		return nil, errEmptyFile
	}
	return body, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}