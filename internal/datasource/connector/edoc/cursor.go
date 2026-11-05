package edoc

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

const cursorSchemaVersion = 1

// fileCursor remembers one file's current-version fingerprint and the display
// name used when a later run needs to report its deletion.
type fileCursor struct {
	FP   string `json:"fp"`
	Name string `json:"name,omitempty"`
}

// cursor is the connector state persisted between runs. Files maps every known
// file ID to its fingerprint; an empty fingerprint marks a file whose fetch
// failed and must be retried. FullSync and FullSyncBaseline exist only while a
// forced full run is in progress (same shape as Seafile/Confluence).
type cursor struct {
	SchemaVersion    int                   `json:"schema_version"`
	Roots            []string              `json:"roots"`
	RootNames        map[string]string     `json:"root_names,omitempty"`
	Files            map[string]fileCursor `json:"files"`
	FullSync         bool                  `json:"full_sync,omitempty"`
	FullSyncBaseline map[string]fileCursor `json:"full_sync_baseline,omitempty"`
}

// decodeCursor accepts a nil or empty cursor as a fresh state; anything else
// must carry the current schema version.
func decodeCursor(old *types.SyncCursor) (cursor, error) {
	c := cursor{SchemaVersion: cursorSchemaVersion}
	if old != nil && len(old.ConnectorCursor) > 0 {
		c.SchemaVersion = 0
		raw, err := json.Marshal(old.ConnectorCursor)
		if err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			return cursor{}, fmt.Errorf("%w: invalid edoc cursor: %w", datasource.ErrInvalidConfig, err)
		}
		if c.SchemaVersion != cursorSchemaVersion {
			return cursor{}, fmt.Errorf("%w: unsupported edoc cursor schema %d",
				datasource.ErrInvalidConfig, c.SchemaVersion)
		}
	}
	if c.Files == nil {
		c.Files = make(map[string]fileCursor)
	}
	if c.FullSyncBaseline == nil {
		c.FullSyncBaseline = make(map[string]fileCursor)
	}
	return c, nil
}

// toSyncCursor produces a snapshot independent of the working maps through a
// JSON round trip, so the service can serialize it after the run moves on.
func (c *cursor) toSyncCursor() *types.SyncCursor {
	raw, _ := json.Marshal(c)
	fields := map[string]interface{}{}
	_ = json.Unmarshal(raw, &fields)
	return &types.SyncCursor{LastSyncTime: time.Now().UTC(), ConnectorCursor: fields}
}

// prepareCursor decodes the previous cursor, refuses an unrelated selection
// and switches into full-sync mode when requested. The returned flag reports
// whether the cursor changed and therefore needs saving.
func prepareCursor(old *types.SyncCursor, roots []int64, forceFull bool) (cursor, bool, error) {
	c, err := decodeCursor(old)
	if err != nil {
		return cursor{}, false, err
	}
	selected := make([]string, 0, len(roots))
	for _, root := range roots {
		selected = append(selected, folderKey(root))
	}
	if len(c.Roots) > 0 && !sameRootSet(c.Roots, selected) && !overlaps(c.Roots, selected) {
		return cursor{}, false, fmt.Errorf(
			"%w: this data source already synced eDoc folder(s) %s; add or remove folders from that "+
				"selection, or create a new data source instead of pointing it at %s",
			datasource.ErrInvalidConfig, describeRoots(c), strings.Join(selected, ", "))
	}
	changed := false
	if !sameRootSet(c.Roots, selected) {
		c.Roots = selected
		changed = true
	}
	if c.RootNames == nil {
		c.RootNames = make(map[string]string, len(selected))
	}
	if forceFull && !c.FullSync {
		c.FullSyncBaseline = maps.Clone(c.Files)
		c.Files = make(map[string]fileCursor)
		c.FullSync = true
		changed = true
	}
	return c, changed, nil
}

func sameRootSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, v := range a {
		set[v] = true
	}
	for _, v := range b {
		if !set[v] {
			return false
		}
	}
	return true
}

// overlaps reports whether the new selection shares at least one folder with
// the recorded one. A completely disjoint selection is treated as pointing the
// data source at a different library, which would silently delete everything
// previously synced.
func overlaps(recorded, selected []string) bool {
	set := make(map[string]bool, len(selected))
	for _, v := range selected {
		set[v] = true
	}
	for _, v := range recorded {
		if set[v] {
			return true
		}
	}
	return false
}

func describeRoots(c cursor) string {
	names := make([]string, 0, len(c.Roots))
	for _, id := range c.Roots {
		if name := c.RootNames[id]; name != "" {
			names = append(names, fmt.Sprintf("%s(%s)", name, id))
			continue
		}
		names = append(names, id)
	}
	return strings.Join(names, ", ")
}

// folderIDOf converts a cursor root back to its numeric id; unparsable entries
// are skipped (they can only come from a hand-edited cursor).
func folderIDOf(key string) (int64, bool) {
	n, err := strconv.ParseInt(key, 10, 64)
	return n, err == nil && n > 0
}