package edoc

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	_ datasource.Connector              = (*Connector)(nil)
	_ datasource.StreamingConnector     = (*Connector)(nil)
	_ datasource.FullStreamingConnector = (*Connector)(nil)
)

// maxAncestorDepth bounds the parent walk used to reveal a saved selection in
// a lazily loaded picker.
const maxAncestorDepth = 64

// Connector is stateless: every data source carries its own eDoc base URL and
// integration key in its encrypted credentials.
type Connector struct{}

// NewConnector creates the eDoc connector registered in the container.
func NewConnector() *Connector { return &Connector{} }

// Type returns the connector type key stored on data sources.
func (*Connector) Type() string { return types.ConnectorTypeEDoc }

// Validate logs in with the integration key and, when folders are selected,
// confirms each one is readable by the account. It never lists a subtree.
func (*Connector) Validate(ctx context.Context, ds *types.DataSourceConfig) error {
	cfg, err := parseConfig(ds)
	if err != nil {
		return err
	}
	if err := validateSettings(ds); err != nil {
		return err
	}
	roots, err := selectedRoots(ds.ResourceIDs)
	if err != nil {
		return err
	}
	client := newClient(cfg)
	if _, err := client.login(ctx); err != nil {
		return err
	}
	for _, root := range roots {
		if _, err := client.folderInfo(ctx, root); err != nil {
			return mapFolderError(err, root)
		}
	}
	return nil
}

// ListResources returns the library roots for an empty parent and one folder
// level otherwise. Only folders are offered: a data source mirrors folder
// subtrees, so individual files are selected implicitly through their folder.
func (*Connector) ListResources(
	ctx context.Context, ds *types.DataSourceConfig, parentID string,
) ([]types.Resource, error) {
	cfg, err := parseConfig(ds)
	if err != nil {
		return nil, err
	}
	client := newClient(cfg)

	var out []types.Resource
	if strings.TrimSpace(parentID) == "" {
		out, err = listRoots(ctx, client)
	} else {
		parent, parseErr := parseFolderID(parentID)
		if parseErr != nil {
			return nil, parseErr
		}
		out, err = listChildFolders(ctx, client, parent)
	}
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ExternalID < out[j].ExternalID
	})
	return out, nil
}

// listRoots resolves the enterprise and personal libraries through
// Doc/GetTopFolderId so a redeployed instance with different root IDs still
// works.
func listRoots(ctx context.Context, client *client) ([]types.Resource, error) {
	roots, err := client.topFolders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]types.Resource, 0, 2)
	if roots.TopPublicID > 0 {
		resource, err := rootResource(ctx, client, roots.TopPublicID)
		if err != nil {
			return nil, err
		}
		out = append(out, resource)
	}
	if roots.TopPersonalID > 0 {
		// The integration account may have no personal library; that is not a
		// failure, the enterprise library is still selectable.
		if resource, err := rootResource(ctx, client, roots.TopPersonalID); err == nil {
			out = append(out, resource)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no accessible eDoc library", datasource.ErrResourceNotFound)
	}
	return out, nil
}

func rootResource(ctx context.Context, client *client, id int64) (types.Resource, error) {
	info, err := client.folderInfo(ctx, id)
	if err != nil {
		return types.Resource{}, mapFolderError(err, id)
	}
	return folderResource(info, ""), nil
}

func listChildFolders(ctx context.Context, client *client, parent int64) ([]types.Resource, error) {
	children, err := client.listChildren(ctx, parent)
	if err != nil {
		return nil, mapFolderError(err, parent)
	}
	parentID := folderKey(parent)
	out := make([]types.Resource, 0, len(children))
	for _, child := range children {
		if child.IsDeleted {
			continue
		}
		out = append(out, folderResource(child, parentID))
	}
	return out, nil
}

func folderResource(info folderInfo, parentID string) types.Resource {
	return types.Resource{
		ExternalID:  folderKey(info.ID),
		Name:        info.Name,
		Type:        "folder",
		Description: fmt.Sprintf("%d folders · %d files", info.folders(), info.files()),
		ParentID:    parentID,
		HasChildren: info.folders() > 0,
		ModifiedAt:  parseEdocTime(info.ModifyTime),
	}
}

// mapFolderError adds scope context and separates a missing folder from a
// permission problem. Both abort a run rather than emit deletions.
func mapFolderError(err error, id int64) error {
	var statusErr *httpError
	switch {
	case errors.As(err, &statusErr):
		switch statusErr.Status {
		case http.StatusNotFound:
			return fmt.Errorf("%w: edoc folder %d", datasource.ErrResourceNotFound, id)
		case http.StatusForbidden:
			return fmt.Errorf("%w: %s: edoc folder %d", datasource.ErrFetchFailed, reasonPermissionDenied, id)
		}
	}
	if errors.Is(err, datasource.ErrResourceNotFound) {
		return err
	}
	return fmt.Errorf("edoc folder %d: %w", id, err)
}

// ResolveResourceAncestors walks each selection's parents so a lazily loaded
// picker can reveal a pre-existing (possibly deeply nested) selection. eDoc
// exposes the parent chain only through folder metadata, so this costs one
// request per level.
func (*Connector) ResolveResourceAncestors(
	ctx context.Context, ds *types.DataSourceConfig, ids []string,
) ([]string, error) {
	cfg, err := parseConfig(ds)
	if err != nil {
		return nil, err
	}
	client := newClient(cfg)

	seen := make(map[int64]bool)
	ancestors := make([]int64, 0, len(ids))
	for _, raw := range ids {
		current, parseErr := parseFolderID(raw)
		if parseErr != nil {
			continue
		}
		for depth := 0; depth < maxAncestorDepth; depth++ {
			info, infoErr := client.folderInfo(ctx, current)
			if infoErr != nil {
				break
			}
			parent := info.ParentID
			if parent <= 0 || parent == current {
				break
			}
			if !seen[parent] {
				seen[parent] = true
				ancestors = append(ancestors, parent)
			}
			current = parent
		}
	}
	sort.Slice(ancestors, func(i, j int) bool { return ancestors[i] < ancestors[j] })
	out := make([]string, 0, len(ancestors))
	for _, id := range ancestors {
		out = append(out, folderKey(id))
	}
	return out, nil
}

// collector adapts the streaming engine to the collecting Connector methods.
type collector struct{ items []types.FetchedItem }

func (h *collector) Emit(_ context.Context, item types.FetchedItem) error {
	h.items = append(h.items, item)
	return nil
}

func (*collector) Checkpoint(context.Context, *types.SyncCursor) error { return nil }

// FetchAll collects a fresh sync of the given resources without a cursor.
func (c *Connector) FetchAll(
	ctx context.Context, ds *types.DataSourceConfig, resourceIDs []string,
) ([]types.FetchedItem, error) {
	if ds == nil {
		return nil, datasource.ErrInvalidConfig
	}
	dsCopy := *ds
	dsCopy.ResourceIDs = resourceIDs
	items, _, err := c.FetchIncremental(ctx, &dsCopy, nil)
	return items, err
}

// FetchIncremental collects one streaming run into memory.
func (c *Connector) FetchIncremental(
	ctx context.Context, ds *types.DataSourceConfig, old *types.SyncCursor,
) ([]types.FetchedItem, *types.SyncCursor, error) {
	h := &collector{}
	next, err := c.FetchStream(ctx, ds, old, h)
	return h.items, next, err
}

// FetchStream is the production sync path: it walks the selected folders,
// emits changed files as they are downloaded and checkpoints the cursor so an
// interrupted run resumes instead of restarting.
func (c *Connector) FetchStream(
	ctx context.Context, ds *types.DataSourceConfig, old *types.SyncCursor, h datasource.StreamHandler,
) (*types.SyncCursor, error) {
	return c.runSync(ctx, ds, old, h, false)
}

// FetchFullStream re-downloads every file while keeping the old cursor as a
// deletion baseline until the run completes.
func (c *Connector) FetchFullStream(
	ctx context.Context, ds *types.DataSourceConfig, old *types.SyncCursor, h datasource.StreamHandler,
) (*types.SyncCursor, error) {
	return c.runSync(ctx, ds, old, h, true)
}