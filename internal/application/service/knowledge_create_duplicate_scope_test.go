package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestUsesSourceIdentityDuplicateCheck pins the set of channels whose files are
// deduplicated per datasource_id + external_id instead of by content hash. A
// folder-mirroring connector must be in this set: edoc bid folders routinely
// hold byte-identical decks under different names, and content-hash dedupe
// would silently drop all but the first.
func TestUsesSourceIdentityDuplicateCheck(t *testing.T) {
	for _, channel := range []string{
		types.ConnectorTypeGitLab,
		types.ChannelConfluence,
		types.ChannelSeafile,
		types.ChannelEdoc,
	} {
		require.Truef(t, usesSourceIdentityDuplicateCheck(channel), "%s must dedupe per source identity", channel)
	}
	for _, channel := range []string{types.ChannelWeb, types.ChannelAPI, types.ChannelFeishu, ""} {
		require.Falsef(t, usesSourceIdentityDuplicateCheck(channel),
			"%s must keep content-hash dedupe", channel)
	}
}
