package edoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

// TestMain whitelists loopback for SSRF so the httptest servers are reachable.
// Production keeps the default strict policy.
func TestMain(m *testing.M) {
	_ = os.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost")
	utils.ResetSSRFWhitelistForTest()
	os.Exit(m.Run())
}

const fakeIntegrationKey = "c8383832-test-key"

// fakeFile is one file the fake eDoc serves from a folder.
type fakeFile struct {
	ID        int64
	Name      string
	VerID     int64
	Size      int64
	ModifyAt  string
	Body      []byte
	Denied    bool // DownLoadCheck answers nResult 5
	IsDeleted bool
}

// fakeFolder is one folder node in the fake tree.
type fakeFolder struct {
	Info     folderInfo
	Children []int64
	Files    []fakeFile
	// ListError makes the folder's child listing fail with a result code.
	ListError bool
}

// fakeEdoc is an in-process stand-in for an eDoc SDK API: integration login,
// folder tree, paged file listing, the download gate and the byte endpoint.
type fakeEdoc struct {
	server *httptest.Server

	mu         sync.Mutex
	token      string
	loginCalls int
	// expirePending answers the next N authenticated calls with the
	// {"errorCode":"4"} envelope eDoc uses for an expired session.
	expirePending int
	checkCalls    int
	downloadCalls int
	listCalls     int
	folders       map[int64]*fakeFolder
	roots         rootFolders
}

func newFakeEdoc(t *testing.T) *fakeEdoc {
	t.Helper()
	f := &fakeEdoc{
		token:   "tok-1",
		folders: map[int64]*fakeFolder{},
		roots:   rootFolders{TopPublicID: 1, TopPersonalID: 900},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeEdoc) baseURL() string { return f.server.URL }

func (f *fakeEdoc) config(resourceIDs ...string) *types.DataSourceConfig {
	return &types.DataSourceConfig{
		Type: types.ConnectorTypeEDoc,
		Credentials: map[string]interface{}{
			"base_url":        f.baseURL(),
			"login_name":      "aiuser",
			"integration_key": fakeIntegrationKey,
		},
		ResourceIDs: resourceIDs,
	}
}

func (f *fakeEdoc) addFolder(info folderInfo, files ...fakeFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if info.ChildFileCount == 0 && len(files) > 0 {
		info.ChildFileCount = len(files)
	}
	if info.ModifyTime == "" {
		info.ModifyTime = "2026-01-04 16:44:47"
	}
	f.folders[info.ID] = &fakeFolder{Info: info, Files: files}
}

func (f *fakeEdoc) link(parentID, childID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if parent, ok := f.folders[parentID]; ok {
		parent.Children = append(parent.Children, childID)
		parent.Info.ChildFolderCount = len(parent.Children)
	}
}

func (f *fakeEdoc) setFiles(folderID int64, files ...fakeFile) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if folder, ok := f.folders[folderID]; ok {
		folder.Files = files
		folder.Info.ChildFileCount = len(files)
	}
}

func (f *fakeEdoc) counts() (login, check, download, list int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginCalls, f.checkCalls, f.downloadCalls, f.listCalls
}

func (f *fakeEdoc) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Path == "/api/services/org/UserLoginIntegrationByUserLoginName" {
		f.handleLogin(w, r)
		return
	}
	if !f.authorized(r) {
		writeJSON(w, map[string]string{"errorCode": "4", "errorMsg": "Token失效"})
		return
	}
	if f.expirePending > 0 {
		f.expirePending--
		writeJSON(w, map[string]string{"errorCode": "4", "errorMsg": "Token失效"})
		return
	}

	switch r.URL.Path {
	case "/api/services/Doc/GetTopFolderId":
		writeJSON(w, map[string]interface{}{"result": 0, "data": f.roots})
	case "/api/services/Folder/GetFolderInfoById":
		f.handleFolderInfo(w, r)
	case "/api/services/Folder/GetChildrenFolders":
		f.handleChildren(w, r)
	case "/api/services/File/GetChildFilePageListByFolderId":
		f.handleFilePage(w, r)
	case "/DownLoad/DownLoadCheck":
		f.handleDownloadCheck(w, r)
	case "/downLoad/index":
		f.handleDownload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeEdoc) handleLogin(w http.ResponseWriter, r *http.Request) {
	f.loginCalls++
	var body struct {
		LoginName      string `json:"loginName"`
		IntegrationKey string `json:"integrationKey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.IntegrationKey != fakeIntegrationKey {
		writeJSON(w, map[string]interface{}{"result": 1, "message": "integration key rejected"})
		return
	}
	writeJSON(w, map[string]interface{}{"result": 0, "data": f.token})
}

func (f *fakeEdoc) authorized(r *http.Request) bool {
	return r.URL.Query().Get("token") == f.token
}

func (f *fakeEdoc) handleFolderInfo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("folderId"), 10, 64)
	folder, ok := f.folders[id]
	if !ok {
		writeJSON(w, map[string]interface{}{"result": 801, "message": "文件夹不存在"})
		return
	}
	writeJSON(w, map[string]interface{}{"result": 0, "data": folder.Info})
}

func (f *fakeEdoc) handleChildren(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("topFolderId"), 10, 64)
	folder, ok := f.folders[id]
	if !ok || folder.ListError {
		writeJSON(w, map[string]interface{}{"result": 500, "message": "listing failed"})
		return
	}
	children := make([]folderInfo, 0, len(folder.Children))
	for _, childID := range folder.Children {
		if child, ok := f.folders[childID]; ok {
			children = append(children, child.Info)
		}
	}
	writeJSON(w, map[string]interface{}{"result": 0, "data": children})
}

func (f *fakeEdoc) handleFilePage(w http.ResponseWriter, r *http.Request) {
	f.listCalls++
	query := r.URL.Query()
	id, _ := strconv.ParseInt(query.Get("folderId"), 10, 64)
	page, _ := strconv.Atoi(query.Get("pageIndex"))
	size, _ := strconv.Atoi(query.Get("pageSize"))
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 10
	}
	folder, ok := f.folders[id]
	if !ok {
		writeJSON(w, map[string]interface{}{"Result": 1, "TotalCount": 0})
		return
	}
	start := (page - 1) * size
	if start > len(folder.Files) {
		start = len(folder.Files)
	}
	end := start + size
	if end > len(folder.Files) {
		end = len(folder.Files)
	}
	files := make([]fileInfo, 0, end-start)
	for _, file := range folder.Files[start:end] {
		files = append(files, fileInfo{
			ID: file.ID, Name: file.Name, ExtName: fileExt(file.Name),
			Guid: fmt.Sprintf("guid-%d", file.ID), CurSize: file.Size, LastSize: file.Size,
			LastVerID: file.VerID, ModifyTime: file.ModifyAt, FilePath: folder.Info.Path + "\\",
			IsDeleted: file.IsDeleted, FileType: 2,
		})
	}
	writeJSON(w, map[string]interface{}{
		"Files": files, "TotalCount": len(folder.Files), "Result": 0, "Code": 0,
	})
}

func (f *fakeEdoc) handleDownloadCheck(w http.ResponseWriter, r *http.Request) {
	f.checkCalls++
	id, _ := strconv.ParseInt(r.URL.Query().Get("fileIds"), 10, 64)
	file, ok := f.findFile(id)
	if !ok {
		writeJSON(w, map[string]interface{}{"nResult": 601, "msg": "file not found"})
		return
	}
	if file.Denied {
		writeJSON(w, map[string]interface{}{"nResult": 5, "msg": ""})
		return
	}
	writeJSON(w, map[string]interface{}{"nResult": 0, "RegionHash": fmt.Sprintf("rh:%d", id)})
}

func (f *fakeEdoc) handleDownload(w http.ResponseWriter, r *http.Request) {
	f.downloadCalls++
	hash := r.URL.Query().Get("regionHash")
	id, err := strconv.ParseInt(strings.TrimPrefix(hash, "rh:"), 10, 64)
	if err != nil {
		http.Error(w, "bad region hash", http.StatusBadRequest)
		return
	}
	file, ok := f.findFile(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(file.Body)
}

func (f *fakeEdoc) findFile(id int64) (fakeFile, bool) {
	for _, folder := range f.folders {
		for _, file := range folder.Files {
			if file.ID == id {
				return file, true
			}
		}
	}
	return fakeFile{}, false
}

func writeJSON(w http.ResponseWriter, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func fileExt(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ""
}

// pptxFile builds a fake file whose body is unique per id.
func pptxFile(id int64, name string, verID int64) fakeFile {
	body := []byte(fmt.Sprintf("body-of-%d", id))
	return fakeFile{
		ID: id, Name: name, VerID: verID, Size: int64(len(body)),
		ModifyAt: "2026-01-04 16:44:47", Body: body,
	}
}

// seedBidTree builds 1 (PublicRoot) → 204038 (12技术解决方案) → 204100 (竞标PPT)
// with the given files in 竞标PPT.
func seedBidTree(f *fakeEdoc, files ...fakeFile) {
	f.addFolder(folderInfo{ID: 1, Name: "PublicRoot", Path: "1", ChildFolderCount: 1})
	f.addFolder(folderInfo{ID: 204038, Name: "12技术解决方案", ParentID: 1, Path: "1\\204038"})
	f.addFolder(folderInfo{ID: 204100, Name: "竞标PPT", ParentID: 204038, Path: "1\\204038\\204100", ChildFileCount: len(files)}, files...)
	f.link(1, 204038)
	f.link(204038, 204100)
}

func TestValidate(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1))
	connector := NewConnector()

	require.NoError(t, connector.Validate(context.Background(), fake.config("204100")))

	loginCalls, _, _, _ := fake.counts()
	require.Equal(t, 1, loginCalls)

	badKey := fake.config("204100")
	badKey.Credentials["integration_key"] = "wrong-key"
	err := connector.Validate(context.Background(), badKey)
	require.ErrorIs(t, err, datasource.ErrInvalidCredentials)

	missing := fake.config("204100")
	missing.Credentials = map[string]interface{}{}
	require.ErrorIs(t, connector.Validate(context.Background(), missing), datasource.ErrInvalidConfig)

	noBase := fake.config("204100")
	delete(noBase.Credentials, "base_url")
	require.ErrorIs(t, connector.Validate(context.Background(), noBase), datasource.ErrInvalidConfig)
}

func TestValidateRejectsHistoricalVersions(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake)
	ds := fake.config("204100")
	ds.Settings = map[string]interface{}{"current_version_only": false}
	require.ErrorIs(t, NewConnector().Validate(context.Background(), ds), datasource.ErrInvalidConfig)

	ds.Settings = map[string]interface{}{"current_version_only": true}
	require.NoError(t, NewConnector().Validate(context.Background(), ds))
}

func TestListResources(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1))
	fake.addFolder(folderInfo{ID: 900, Name: "个人空间", Path: "2\\900"})
	connector := NewConnector()

	roots, err := connector.ListResources(context.Background(), fake.config(), "")
	require.NoError(t, err)
	require.Len(t, roots, 2)
	require.Equal(t, "1", roots[0].ExternalID)
	require.Equal(t, "PublicRoot", roots[0].Name)
	require.True(t, roots[0].HasChildren)
	require.Equal(t, "900", roots[1].ExternalID)

	children, err := connector.ListResources(context.Background(), fake.config(), "1")
	require.NoError(t, err)
	require.Len(t, children, 1)
	require.Equal(t, "204038", children[0].ExternalID)
	require.Equal(t, "1", children[0].ParentID)
	require.Equal(t, "12技术解决方案", children[0].Name)
	require.True(t, children[0].HasChildren)

	leaf, err := connector.ListResources(context.Background(), fake.config(), "204100")
	require.NoError(t, err)
	require.Empty(t, leaf, "a leaf folder has no sub-folders to select")

	_, err = connector.ListResources(context.Background(), fake.config(), "not-a-number")
	require.ErrorIs(t, err, datasource.ErrInvalidConfig)
}

func TestResolveResourceAncestors(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1))
	connector := NewConnector()

	ancestors, err := connector.ResolveResourceAncestors(context.Background(), fake.config(), []string{"204100"})
	require.NoError(t, err)
	require.Equal(t, []string{"1", "204038"}, ancestors)

	ancestors, err = connector.ResolveResourceAncestors(context.Background(), fake.config(), []string{"1", "bogus"})
	require.NoError(t, err)
	require.Empty(t, ancestors)
}

func TestFetchAllBuildsItems(t *testing.T) {
	fake := newFakeEdoc(t)
	file := pptxFile(10, "竞标材料.pptx", 7)
	seedBidTree(fake, file)
	fake.addFolder(folderInfo{ID: 204200, Name: "子目录", ParentID: 204100, Path: "1\\204038\\204100\\204200"},
		fakeFileAt(11, "nested.pdf", 3, "1\\204038\\204100\\204200\\"))
	fake.link(204100, 204200)

	connector := NewConnector()
	items, err := connector.FetchAll(context.Background(), fake.config("204100"), []string{"204100"})
	require.NoError(t, err)
	require.Len(t, items, 2)

	byID := map[string]types.FetchedItem{}
	for _, item := range items {
		byID[item.ExternalID] = item
	}
	root := byID["edoc:file:10"]
	require.Equal(t, "竞标材料.pptx", root.Title)
	require.Equal(t, "竞标PPT/竞标材料.pptx", root.FileName)
	require.Equal(t, string(file.Body), string(root.Content))
	require.Equal(t, "application/vnd.openxmlformats-officedocument.presentationml.presentation", root.ContentType)
	require.Equal(t, "204100", root.SourceResourceID)
	require.Equal(t, types.ChannelEdoc, root.Metadata["channel"])
	require.Equal(t, "7", root.Metadata["edoc_ver_id"])
	require.Equal(t, "1\\204038\\204100\\", root.Metadata["edoc_path"])
	require.Equal(t, 2026, root.UpdatedAt.Year())

	nested := byID["edoc:file:11"]
	require.Equal(t, "竞标PPT/子目录/nested.pdf", nested.FileName)
}

func fakeFileAt(id int64, name string, verID int64, path string) fakeFile {
	body := []byte(fmt.Sprintf("body-of-%d", id))
	return fakeFile{
		ID: id, Name: name, VerID: verID, Size: int64(len(body)),
		ModifyAt: "2026-02-05 09:00:00", Body: body,
	}
}

func TestFetchStreamSkipsUnchangedAndRetriesOnNewVersion(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1), pptxFile(11, "b.pptx", 1))
	connector := NewConnector()
	ctx := context.Background()

	h := &collector{}
	cursor, err := connector.FetchStream(ctx, fake.config("204100"), nil, h)
	require.NoError(t, err)
	require.Len(t, h.items, 2)
	_, _, downloads, _ := fake.counts()
	require.Equal(t, 2, downloads)

	// Second run: nothing changed, so nothing is downloaded or emitted.
	h2 := &collector{}
	cursor2, err := connector.FetchStream(ctx, fake.config("204100"), cursor, h2)
	require.NoError(t, err)
	require.Empty(t, h2.items)
	_, _, downloads2, _ := fake.counts()
	require.Equal(t, 2, downloads2, "unchanged files must not be re-downloaded")

	// A new version of one file is picked up.
	fake.setFiles(204100, pptxFile(10, "a.pptx", 2), pptxFile(11, "b.pptx", 1))
	h3 := &collector{}
	_, err = connector.FetchStream(ctx, fake.config("204100"), cursor2, h3)
	require.NoError(t, err)
	require.Len(t, h3.items, 1)
	require.Equal(t, "edoc:file:10", h3.items[0].ExternalID)
}

func TestDeletionIsReconciled(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1), pptxFile(11, "b.pptx", 1))
	connector := NewConnector()
	ctx := context.Background()

	cursor, err := connector.FetchStream(ctx, fake.config("204100"), nil, &collector{})
	require.NoError(t, err)

	fake.setFiles(204100, pptxFile(10, "a.pptx", 1))
	h := &collector{}
	cursor, err = connector.FetchStream(ctx, fake.config("204100"), cursor, h)
	require.NoError(t, err)
	require.Len(t, h.items, 1)
	require.True(t, h.items[0].IsDeleted)
	require.Equal(t, "edoc:file:11", h.items[0].ExternalID)

	// The deletion is not repeated on the next run.
	h2 := &collector{}
	_, err = connector.FetchStream(ctx, fake.config("204100"), cursor, h2)
	require.NoError(t, err)
	require.Empty(t, h2.items)
}

func TestPermissionDeniedEmitsErrorItemAndRetries(t *testing.T) {
	fake := newFakeEdoc(t)
	denied := pptxFile(10, "denied.pptx", 1)
	denied.Denied = true
	seedBidTree(fake, denied)
	connector := NewConnector()
	ctx := context.Background()

	h := &collector{}
	cursor, err := connector.FetchStream(ctx, fake.config("204100"), nil, h)
	require.NoError(t, err)
	require.Len(t, h.items, 1)
	require.Empty(t, h.items[0].Content)
	require.Equal(t, reasonPermissionDenied, h.items[0].Metadata["error_reason_code"])
	require.NotEmpty(t, h.items[0].Metadata["error"])

	// The file stays known with an empty fingerprint, so the next run retries
	// instead of treating it as deleted.
	h2 := &collector{}
	_, err = connector.FetchStream(ctx, fake.config("204100"), cursor, h2)
	require.NoError(t, err)
	require.Len(t, h2.items, 1)
	require.False(t, h2.items[0].IsDeleted)
	_, checks, _, _ := fake.counts()
	require.Equal(t, 2, checks)
}

func TestPaginationAcrossPages(t *testing.T) {
	fake := newFakeEdoc(t)
	files := make([]fakeFile, 0, 250)
	for i := 0; i < 250; i++ {
		files = append(files, pptxFile(int64(1000+i), fmt.Sprintf("f-%03d.pptx", i), 1))
	}
	seedBidTree(fake, files...)

	connector := NewConnector()
	items, err := connector.FetchAll(context.Background(), fake.config("204100"), []string{"204100"})
	require.NoError(t, err)
	require.Len(t, items, 250)
	_, _, _, lists := fake.counts()
	require.Equal(t, 3, lists, "250 files at 100 per page")
}

func TestTokenExpiredRelogin(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1))
	fake.mu.Lock()
	fake.expirePending = 1
	fake.token = "tok-2"
	fake.mu.Unlock()

	connector := NewConnector()
	_, err := connector.FetchAll(context.Background(), fake.config("204100"), []string{"204100"})
	require.NoError(t, err)

	loginCalls, _, _, _ := fake.counts()
	require.Equal(t, 2, loginCalls, "one initial login plus one refresh")
}

func TestFileTooLargeIsReportedWithoutDownload(t *testing.T) {
	t.Setenv("MAX_FILE_SIZE_MB", "1")
	fake := newFakeEdoc(t)
	big := pptxFile(10, "huge.pptx", 1)
	big.Size = 2 << 20
	big.Body = make([]byte, 16)
	seedBidTree(fake, big)

	connector := NewConnector()
	items, err := connector.FetchAll(context.Background(), fake.config("204100"), []string{"204100"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, reasonFileTooLarge, items[0].Metadata["error_reason_code"])
	_, _, downloads, _ := fake.counts()
	require.Zero(t, downloads)
}

func TestUnsupportedExtensionIsSkipped(t *testing.T) {
	fake := newFakeEdoc(t)
	archive := fakeFile{ID: 12, Name: "bundle.zip", VerID: 1, Size: 4, ModifyAt: "2026-01-04 16:44:47", Body: []byte("zip!")}
	seedBidTree(fake, archive, pptxFile(10, "a.pptx", 1))

	items, err := NewConnector().FetchAll(context.Background(), fake.config("204100"), []string{"204100"})
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "edoc:file:10", items[0].ExternalID)
}

func TestListingFailureAbortsWithoutDeletions(t *testing.T) {
	fake := newFakeEdoc(t)
	seedBidTree(fake, pptxFile(10, "a.pptx", 1))
	connector := NewConnector()
	ctx := context.Background()

	cursor, err := connector.FetchStream(ctx, fake.config("204100"), nil, &collector{})
	require.NoError(t, err)

	fake.mu.Lock()
	fake.folders[204100].ListError = true
	fake.mu.Unlock()

	h := &collector{}
	_, err = connector.FetchStream(ctx, fake.config("204100"), cursor, h)
	require.Error(t, err)
	require.Empty(t, h.items, "a failed listing must not look like a mass deletion")
}

func TestCursorRejectsDisjointSelection(t *testing.T) {
	old := &types.SyncCursor{ConnectorCursor: map[string]interface{}{
		"schema_version": float64(cursorSchemaVersion),
		"roots":          []interface{}{"204100"},
		"files":          map[string]interface{}{},
	}}
	_, _, err := prepareCursor(old, []int64{999999}, false)
	require.ErrorIs(t, err, datasource.ErrInvalidConfig)

	_, changed, err := prepareCursor(old, []int64{204100, 204200}, false)
	require.NoError(t, err)
	require.True(t, changed, "adding a folder updates the recorded roots")
}

func TestCursorRoundTrip(t *testing.T) {
	cur := cursor{
		SchemaVersion: cursorSchemaVersion,
		Roots:         []string{"204100"},
		RootNames:     map[string]string{"204100": "竞标PPT"},
		Files:         map[string]fileCursor{"10": {FP: "v:1|m:x|s:3", Name: "a.pptx"}},
	}
	snapshot := cur.toSyncCursor()
	decoded, err := decodeCursor(snapshot)
	require.NoError(t, err)
	require.Equal(t, cur.Roots, decoded.Roots)
	require.Equal(t, cur.Files, decoded.Files)

	tampered := &types.SyncCursor{ConnectorCursor: map[string]interface{}{"schema_version": 99}}
	_, err = decodeCursor(tampered)
	require.ErrorIs(t, err, datasource.ErrInvalidConfig)
	require.True(t, errors.Is(err, datasource.ErrInvalidConfig))
}

func TestNormalizeBaseURL(t *testing.T) {
	got, err := normalizeBaseURL(" files.union-laboratory.com:8866 ")
	require.NoError(t, err)
	require.Equal(t, "https://files.union-laboratory.com:8866", got)

	got, err = normalizeBaseURL("http://example.com/prefix/")
	require.NoError(t, err)
	require.Equal(t, "http://example.com/prefix", got)

	for _, bad := range []string{"", "ftp://example.com", "https://example.com/?a=1", "https://user:pw@example.com"} {
		_, err := normalizeBaseURL(bad)
		require.ErrorIsf(t, err, datasource.ErrInvalidConfig, "base_url %q must be rejected", bad)
	}
}