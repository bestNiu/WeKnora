// Package edoc implements the WeKnora data source connector for eDoc
// (易道 / edoc2 ECM) deployments. A data source selects one or more content
// library folders; the connector walks each selected subtree, downloads the
// current version of every supported file and reconciles deletions against a
// per-file fingerprint cursor.
package edoc

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

// config is the parsed credential set. The selection lives entirely in
// resource_ids, so Settings carry no eDoc-specific keys beyond the version
// guard (see validateSettings).
type config struct {
	baseURL        string
	loginName      string
	integrationKey string
	ipAddress      string
}

// defaultIPAddress is reported to eDoc on integration login. eDoc records it
// for its own audit trail; the value is not used for authorisation.
const defaultIPAddress = "127.0.0.1"

func parseConfig(ds *types.DataSourceConfig) (config, error) {
	if ds == nil {
		return config{}, fmt.Errorf("%w: edoc config is nil", datasource.ErrInvalidConfig)
	}
	baseURL, err := normalizeBaseURL(stringValue(ds.Credentials["base_url"]))
	if err != nil {
		return config{}, err
	}
	loginName := strings.TrimSpace(stringValue(ds.Credentials["login_name"]))
	if loginName == "" {
		return config{}, fmt.Errorf("%w: login_name is required", datasource.ErrInvalidConfig)
	}
	integrationKey := strings.TrimSpace(stringValue(ds.Credentials["integration_key"]))
	if integrationKey == "" {
		return config{}, fmt.Errorf("%w: integration_key is required", datasource.ErrInvalidConfig)
	}
	if hasUnsafeChars(loginName) || hasUnsafeChars(integrationKey) {
		return config{}, fmt.Errorf("%w: login_name and integration_key must not contain whitespace or control characters",
			datasource.ErrInvalidConfig)
	}
	ipAddress := strings.TrimSpace(stringValue(ds.Credentials["ip_address"]))
	if ipAddress == "" {
		ipAddress = defaultIPAddress
	}
	return config{baseURL: baseURL, loginName: loginName, integrationKey: integrationKey, ipAddress: ipAddress}, nil
}

func stringValue(v interface{}) string {
	s, _ := v.(string)
	return s
}

func hasUnsafeChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// normalizeBaseURL trims the URL, defaults the scheme to https, drops the
// trailing slash and keeps any deployment path prefix.
func normalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: base_url is required", datasource.ErrInvalidConfig)
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("%w: base_url must be an http(s) URL without credentials, query or fragment",
			datasource.ErrInvalidConfig)
	}
	base := strings.TrimRight(u.String(), "/")
	if err := datasource.ValidateConnectorBaseURL(base); err != nil {
		return "", fmt.Errorf("%w: %v", datasource.ErrInvalidConfig, err)
	}
	return base, nil
}

// validateSettings enforces the version policy. The connector only ever reads
// a file's current version (FileLastVerId) — historical versions are not
// addressable through this sync path — so an explicit opt-out is rejected
// rather than silently ignored.
func validateSettings(ds *types.DataSourceConfig) error {
	if ds == nil || ds.Settings == nil {
		return nil
	}
	raw, ok := ds.Settings["current_version_only"]
	if !ok {
		return nil
	}
	enabled, ok := raw.(bool)
	if !ok || !enabled {
		return fmt.Errorf("%w: edoc only syncs the current version of each file; "+
			"drop settings.current_version_only or set it to true", datasource.ErrInvalidConfig)
	}
	return nil
}

// selectedRoots dedupes the selection and keeps folder IDs only. eDoc folder
// IDs are stable integers, so a renamed or moved folder keeps its identity.
func selectedRoots(ids []string) ([]int64, error) {
	seen := make(map[int64]bool, len(ids))
	roots := make([]int64, 0, len(ids))
	for _, id := range ids {
		parsed, err := parseFolderID(id)
		if err != nil {
			return nil, err
		}
		if !seen[parsed] {
			seen[parsed] = true
			roots = append(roots, parsed)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i] < roots[j] })
	return roots, nil
}

func parseFolderID(id string) (int64, error) {
	trimmed := strings.TrimSpace(id)
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%w: edoc resource id must be a positive folder id", datasource.ErrInvalidConfig)
	}
	return n, nil
}

func folderKey(id int64) string { return strconv.FormatInt(id, 10) }

// folderInfo is the subset of Folder/GetFolderInfoById and
// Folder/GetChildrenFolders used by the connector. The two endpoints label the
// child counters differently (ChildFolderCount vs FolderChildFoldersCount), so
// both shapes are decoded and folded by folders()/files().
type folderInfo struct {
	ID               int64  `json:"FolderId"`
	Name             string `json:"FolderName"`
	ParentID         int64  `json:"ParentFolderId"`
	Path             string `json:"FolderPath"`
	ChildFolderCount int    `json:"ChildFolderCount"`
	ChildFileCount   int    `json:"ChildFileCount"`
	FolderFolders    int    `json:"FolderChildFoldersCount"`
	FolderFiles      int    `json:"FolderChildFilesCount"`
	ModifyTime       string `json:"FolderModifyTime"`
	FolderType       int    `json:"FolderType"`
	IsDeleted        bool   `json:"IsDeleted"`
}

// folders returns the sub-folder count from whichever envelope carried it.
func (f folderInfo) folders() int {
	if f.ChildFolderCount > 0 {
		return f.ChildFolderCount
	}
	return f.FolderFolders
}

// files returns the direct file count from whichever envelope carried it.
func (f folderInfo) files() int {
	if f.ChildFileCount > 0 {
		return f.ChildFileCount
	}
	return f.FolderFiles
}

// rootFolders is the payload of Doc/GetTopFolderId.
type rootFolders struct {
	TopPublicID   int64 `json:"TopPublicFolderId"`
	TopPersonalID int64 `json:"TopPersonalFolderId"`
}

// fileInfo is one entry of File/GetChildFilePageListByFolderId. Only the
// current-version fields are used: the connector never reads history.
type fileInfo struct {
	ID          int64  `json:"FileId"`
	Name        string `json:"FileName"`
	ExtName     string `json:"FileExtName"`
	Guid        string `json:"FileGuid"`
	CurSize     int64  `json:"FileCurSize"`
	LastSize    int64  `json:"FileLastSize"`
	LastVerID   int64  `json:"FileLastVerId"`
	ModifyTime  string `json:"FileModifyTime"`
	FilePath    string `json:"FilePath"`
	FileState   int    `json:"FileState"`
	IsDeleted   bool   `json:"IsDeleted"`
	FileType    int    `json:"FileType"`
	ContentType string `json:"FileContentType"`
}

// size returns the current version's size, preferring the current-version
// field and falling back to the last-version one.
func (f fileInfo) size() int64 {
	if f.CurSize > 0 {
		return f.CurSize
	}
	return f.LastSize
}

func (f fileInfo) ext() string {
	if f.ExtName != "" {
		return f.ExtName
	}
	if i := strings.LastIndex(f.Name, "."); i >= 0 {
		return f.Name[i:]
	}
	return ""
}

// filePage is the paged envelope returned by GetChildFilePageListByFolderId.
// It does not use the ReturnValueResult wrapper the other SDK endpoints use.
type filePage struct {
	Files      []fileInfo `json:"Files"`
	TotalCount int        `json:"TotalCount"`
	Result     int        `json:"Result"`
	Code       int        `json:"Code"`
}

// apiEnvelope is the ReturnValueResult<T> wrapper shared by most SDK
// endpoints. Result is a pointer because a page response omits it.
type apiEnvelope struct {
	Result    *int            `json:"result"`
	Message   string          `json:"message"`
	Data      json.RawMessage `json:"data"`
	ErrorCode string          `json:"errorCode"`
	ErrorMsg  string          `json:"errorMsg"`
}

// downloadCheck is the reply of DownLoad/DownLoadCheck. A non-zero NResult is
// the download gate's verdict: 5 = no permission, 601 = file missing.
type downloadCheck struct {
	NResult    int    `json:"nResult"`
	Msg        string `json:"msg"`
	RegionID   int    `json:"RegionId"`
	RegionType int    `json:"RegionType"`
	RegionHash string `json:"RegionHash"`
}

// eDoc timestamps are server-local wall-clock strings without a zone. The
// connector preserves the raw value in metadata and only uses the parsed form
// for display ordering, so a zone mismatch cannot corrupt sync state.
var edocTimeLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	time.RFC3339,
}

func parseEdocTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range edocTimeLayouts {
		if t, err := time.ParseInLocation(layout, raw, edocZone()); err == nil {
			return t
		}
	}
	return time.Time{}
}

// edocZone is the deployment timezone eDoc reports wall-clock times in.
func edocZone() *time.Location {
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		return loc
	}
	return time.FixedZone("CST", 8*60*60)
}

// Error reason codes surfaced on error items; the frontend localises them via
// datasource.syncError.<code> and falls back to errorReason text.
const (
	reasonPermissionDenied = "edoc_permission_denied"
	reasonNotFound         = "edoc_not_found"
	reasonFileTooLarge     = "edoc_file_too_large"
	reasonEmptyFile        = "edoc_empty_file"
	reasonInvalidResponse  = "edoc_invalid_response"
	reasonFetchFailed      = "edoc_fetch_failed"
)

var reasonText = map[string]string{
	reasonPermissionDenied: "Permission to download the eDoc file was denied",
	reasonNotFound:         "The eDoc file was not found",
	reasonFileTooLarge:     "The eDoc file exceeds the configured size limit",
	reasonEmptyFile:        "The eDoc file is empty",
	reasonInvalidResponse:  "eDoc returned an unexpected response",
	reasonFetchFailed:      "Failed to fetch the file from eDoc",
}

// errorReason returns the English fallback text for a reason code.
func errorReason(code string) string {
	if text, ok := reasonText[code]; ok {
		return text
	}
	return reasonText[reasonFetchFailed]
}