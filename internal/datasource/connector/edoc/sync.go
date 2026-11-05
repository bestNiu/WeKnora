package edoc

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

// checkpointEvery bounds how many cursor mutations may be lost on a crash.
const checkpointEvery = 50

// scope identifies one selected root folder and the display name frozen on
// the first run, so renaming the folder later does not rewrite every file name.
type scope struct {
	key  string
	name string
}

// walkFrame is one folder pending enumeration plus the path segments from the
// selected root down to it.
type walkFrame struct {
	id  int64
	rel []string
}

// syncRun holds the state of one FetchStream / FetchFullStream execution.
type syncRun struct {
	ctx    context.Context
	client *client
	h      datasource.StreamHandler
	cur    cursor
	limit  int64
	// seen records every supported file listed this run; anything known to
	// the cursor but not seen is a deletion candidate.
	seen    map[string]bool
	unsaved int
}

func (c *Connector) runSync(
	ctx context.Context, ds *types.DataSourceConfig, old *types.SyncCursor,
	h datasource.StreamHandler, forceFull bool,
) (*types.SyncCursor, error) {
	cfg, err := parseConfig(ds)
	if err != nil {
		return nil, err
	}
	if err := validateSettings(ds); err != nil {
		return nil, err
	}
	roots, err := selectedRoots(ds.ResourceIDs)
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		return nil, fmt.Errorf("%w: edoc requires at least one selected folder", datasource.ErrInvalidConfig)
	}
	cur, changed, err := prepareCursor(old, roots, forceFull)
	if err != nil {
		return nil, err
	}

	r := &syncRun{
		ctx: ctx, client: newClient(cfg), h: h, cur: cur,
		limit: utils.GetMaxFileSize(), seen: make(map[string]bool),
	}
	if changed {
		r.unsaved++
	}
	for _, root := range roots {
		if err := r.walkRoot(root); err != nil {
			return nil, err
		}
	}
	if err := r.reconcile(); err != nil {
		return nil, err
	}
	logger.Infof(ctx, "[EDoc] sync finished: %d files in scope, %d tracked", len(r.seen), len(r.cur.Files))
	return r.cur.toSyncCursor(), nil
}

// canceledOr reports cancellation as ErrSyncCanceled and passes other errors
// through unchanged.
func (r *syncRun) canceledOr(err error) error {
	if cause := r.ctx.Err(); cause != nil {
		return errors.Join(datasource.ErrSyncCanceled, cause)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(datasource.ErrSyncCanceled, err)
	}
	return err
}

func (r *syncRun) canceled() error {
	if cause := r.ctx.Err(); cause != nil {
		return errors.Join(datasource.ErrSyncCanceled, cause)
	}
	return nil
}

// abort saves unsaved progress while the context is still valid, then returns
// the source error. Handler errors never come through here.
func (r *syncRun) abort(err error) error {
	if canceled := r.canceledOr(err); canceled != err {
		return canceled
	}
	if r.unsaved > 0 {
		if checkpointErr := r.checkpoint(); checkpointErr != nil {
			return errors.Join(err, checkpointErr)
		}
	}
	return err
}

func (r *syncRun) checkpoint() error {
	if err := r.canceled(); err != nil {
		return err
	}
	if err := r.h.Checkpoint(r.ctx, r.cur.toSyncCursor()); err != nil {
		return err
	}
	r.unsaved = 0
	return nil
}

func (r *syncRun) mutated() error {
	r.unsaved++
	if r.unsaved >= checkpointEvery {
		return r.checkpoint()
	}
	return nil
}

func (r *syncRun) emit(item types.FetchedItem) error {
	if err := r.canceled(); err != nil {
		return err
	}
	if err := r.h.Emit(r.ctx, item); err != nil {
		return r.canceledOr(err)
	}
	return nil
}

// walkRoot enumerates one selected folder depth-first. A failed listing aborts
// the run before any deletion is emitted, so a half-visible tree can never
// look like mass deletion.
func (r *syncRun) walkRoot(root int64) error {
	if err := r.canceled(); err != nil {
		return err
	}
	info, err := r.client.folderInfo(r.ctx, root)
	if err != nil {
		return r.abort(mapFolderError(err, root))
	}
	sc := r.scopeOf(root, info.Name)

	stack := []walkFrame{{id: root}}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		files, err := r.client.listFiles(r.ctx, current.id)
		if err != nil {
			return r.abort(mapFolderError(err, current.id))
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		for _, file := range files {
			if err := r.syncFile(file, current.rel, sc); err != nil {
				return err
			}
		}

		children, err := r.client.listChildren(r.ctx, current.id)
		if err != nil {
			return r.abort(mapFolderError(err, current.id))
		}
		children = liveFolders(children)
		for i := len(children) - 1; i >= 0; i-- {
			rel := make([]string, len(current.rel)+1)
			copy(rel, current.rel)
			rel[len(current.rel)] = children[i].Name
			stack = append(stack, walkFrame{id: children[i].ID, rel: rel})
		}
	}
	return nil
}

// liveFolders filters deleted entries and sorts by name for a deterministic
// traversal order.
func liveFolders(in []folderInfo) []folderInfo {
	out := make([]folderInfo, 0, len(in))
	for _, f := range in {
		if !f.IsDeleted {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// scopeOf freezes the root's display name on the first run.
func (r *syncRun) scopeOf(root int64, apiName string) scope {
	key := folderKey(root)
	if name := r.cur.RootNames[key]; name != "" {
		return scope{key: key, name: name}
	}
	name := datasource.SanitizeFileName(strings.TrimSpace(apiName))
	if name == "" || name == "untitled" {
		name = key
	}
	if r.cur.RootNames == nil {
		r.cur.RootNames = make(map[string]string, 1)
	}
	r.cur.RootNames[key] = name
	// No checkpoint here: every following file mutation persists the map.
	r.unsaved++
	return scope{key: key, name: name}
}

// syncFile downloads and emits one file unless its current-version fingerprint
// is unchanged. The fingerprint is stored only after Emit succeeds.
func (r *syncRun) syncFile(file fileInfo, rel []string, sc scope) error {
	if err := r.canceled(); err != nil {
		return err
	}
	key := strconv.FormatInt(file.ID, 10)
	if r.seen[key] {
		return nil
	}
	r.seen[key] = true
	// Unsupported types are outside the sync scope; leaving them unseen would
	// emit a deletion for a file that is still there.
	if file.IsDeleted || !utils.IsSupportedImportExtension(file.ext()) {
		return nil
	}
	name := displayPath(rel, file.Name)
	fp := fingerprint(file)
	if previous, ok := r.cur.Files[key]; ok && previous.FP == fp {
		return nil
	}
	if file.size() > r.limit {
		return r.failFile(file, name, sc, reasonFileTooLarge)
	}

	check, err := r.client.downloadCheck(r.ctx, file.ID)
	if err != nil {
		return r.fetchError(file, name, sc, err)
	}
	body, err := r.client.download(r.ctx, check.RegionHash, r.limit)
	if err != nil {
		return r.fetchError(file, name, sc, err)
	}
	if err := r.emit(r.buildItem(file, rel, name, sc, body)); err != nil {
		return err
	}
	r.cur.Files[key] = fileCursor{FP: fp, Name: name}
	return r.mutated()
}

// fetchError isolates a per-file failure as an error item. Cancellation ends
// the run instead. Client errors carry only the operation and status, so
// logging them is safe.
func (r *syncRun) fetchError(file fileInfo, name string, sc scope, err error) error {
	if canceled := r.canceledOr(err); canceled != err {
		return canceled
	}
	code := reasonFetchFailed
	var denied *downloadDenied
	switch {
	case errors.As(err, &denied):
		switch denied.NResult {
		case 5:
			code = reasonPermissionDenied
		case 601:
			code = reasonNotFound
		}
	case errors.Is(err, errTooLarge):
		code = reasonFileTooLarge
	case errors.Is(err, errEmptyFile):
		code = reasonEmptyFile
	case errors.Is(err, errInvalidReply):
		code = reasonInvalidResponse
	}
	logger.Warnf(r.ctx, "[EDoc] fetch failed: folder=%s file=%d code=%s: %v", sc.key, file.ID, code, err)
	return r.failFile(file, name, sc, code)
}

func (r *syncRun) failFile(file fileInfo, name string, sc scope, code string) error {
	if err := r.emit(r.errorItem(file, name, sc, code)); err != nil {
		return err
	}
	// Keep a known file alive with an empty fingerprint so the next run retries
	// it and still reconciles it if it disappears.
	key := strconv.FormatInt(file.ID, 10)
	if previous, known := r.cur.Files[key]; known && previous.FP != "" {
		r.cur.Files[key] = fileCursor{FP: "", Name: name}
		return r.mutated()
	}
	if _, inBaseline := r.cur.FullSyncBaseline[key]; inBaseline {
		r.cur.Files[key] = fileCursor{FP: "", Name: name}
		return r.mutated()
	}
	return nil
}

// reconcile emits deletions for every known file not seen this run. It only
// runs after every root enumerated successfully.
func (r *syncRun) reconcile() error {
	if err := r.canceled(); err != nil {
		return err
	}
	missing := make(map[string]fileCursor)
	for key, state := range r.cur.Files {
		if !r.seen[key] {
			missing[key] = state
		}
	}
	if r.cur.FullSync {
		for key, state := range r.cur.FullSyncBaseline {
			if !r.seen[key] {
				missing[key] = state
			}
		}
	}
	keys := make([]string, 0, len(missing))
	for key := range missing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 && r.unsaved > 0 {
		if err := r.checkpoint(); err != nil {
			return err
		}
	}
	for _, key := range keys {
		if err := r.emit(r.deletedItem(key, missing[key])); err != nil {
			return err
		}
		delete(r.cur.Files, key)
		delete(r.cur.FullSyncBaseline, key)
		if err := r.mutated(); err != nil {
			return err
		}
	}
	if r.cur.FullSync {
		r.cur.FullSync = false
		r.cur.FullSyncBaseline = nil
		r.unsaved++
	}
	if r.unsaved > 0 {
		return r.checkpoint()
	}
	return nil
}

func (r *syncRun) buildItem(file fileInfo, rel []string, name string, sc scope, body []byte) types.FetchedItem {
	return types.FetchedItem{
		ExternalID:       externalID(file.ID),
		Title:            file.Name,
		FileName:         knowledgeFileName(sc.name, rel, file.Name),
		Content:          body,
		ContentType:      contentTypeOf(file, body),
		UpdatedAt:        parseEdocTime(file.ModifyTime),
		SourceResourceID: sc.key,
		Metadata: map[string]string{
			"channel":            types.ChannelEdoc,
			"source_type":        "edoc",
			"edoc_file_id":       strconv.FormatInt(file.ID, 10),
			"edoc_file_guid":     file.Guid,
			"edoc_path":          file.FilePath,
			"edoc_ext":           file.ext(),
			"edoc_size":          strconv.FormatInt(file.size(), 10),
			"edoc_ver_id":        strconv.FormatInt(file.LastVerID, 10),
			"edoc_modify_time":   file.ModifyTime,
			"edoc_source_folder": sc.key,
			"edoc_display_path":  name,
		},
	}
}

// errorItem follows the service contract for failed fetches: no content, a
// stable reason code and English fallback text. The text is fixed so
// credentials and signed links never reach sync logs.
func (r *syncRun) errorItem(file fileInfo, name string, sc scope, code string) types.FetchedItem {
	return types.FetchedItem{
		ExternalID:       externalID(file.ID),
		Title:            file.Name,
		FileName:         knowledgeFileName(sc.name, nil, file.Name),
		UpdatedAt:        parseEdocTime(file.ModifyTime),
		SourceResourceID: sc.key,
		Metadata: map[string]string{
			"channel":           types.ChannelEdoc,
			"source_type":       "edoc",
			"edoc_file_id":      strconv.FormatInt(file.ID, 10),
			"edoc_display_path": name,
			"error":             errorReason(code),
			"error_reason_code": code,
			"error_reason":      errorReason(code),
		},
	}
}

func (r *syncRun) deletedItem(key string, state fileCursor) types.FetchedItem {
	title := state.Name
	if title == "" {
		title = key
	}
	return types.FetchedItem{
		ExternalID: externalIDString(key),
		Title:      title,
		IsDeleted:  true,
		Metadata: map[string]string{
			"channel":       types.ChannelEdoc,
			"edoc_file_id":  key,
			"source_type":   "edoc",
			"edoc_display_path": title,
		},
	}
}

func externalID(fileID int64) string { return externalIDString(strconv.FormatInt(fileID, 10)) }

func externalIDString(fileID string) string { return "edoc:file:" + fileID }

// fingerprint identifies a file's current version; eDoc bumps LastVerId on
// every modification, so an equal fingerprint means no work to do.
func fingerprint(file fileInfo) string {
	return fmt.Sprintf("v:%d|m:%s|s:%d", file.LastVerID, file.ModifyTime, file.size())
}

// knowledgeFileName maps a file to the KB folder convention
// "<root folder>/<relative path>/<name>": per-segment sanitising keeps nested
// files with identical names distinct inside one knowledge base.
func knowledgeFileName(rootName string, rel []string, fileName string) string {
	segments := make([]string, 0, len(rel)+2)
	segments = append(segments, datasource.SanitizeFileName(rootName))
	for _, segment := range rel {
		segments = append(segments, datasource.SanitizeFileName(segment))
	}
	ext := path.Ext(fileName)
	segments = append(segments, datasource.SanitizeFileName(strings.TrimSuffix(fileName, ext))+ext)
	return strings.Join(segments, "/")
}

func displayPath(rel []string, name string) string {
	if len(rel) == 0 {
		return name
	}
	segments := make([]string, 0, len(rel)+1)
	segments = append(segments, rel...)
	segments = append(segments, name)
	return strings.Join(segments, "/")
}

func contentTypeOf(file fileInfo, body []byte) string {
	if ct := mime.TypeByExtension(strings.ToLower(file.ext())); ct != "" {
		return ct
	}
	return http.DetectContentType(body)
}