package edoc

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

// TestRealEDocReadOnly is an opt-in check against a live eDoc deployment. It
// only reads: integration login, one folder listing and the first two file
// downloads of the configured folder. Enable with:
//
//	EDOC_BASE_URL=https://edoc.example.com:8866 \
//	EDOC_LOGIN_NAME=aiuser EDOC_INTEGRATION_KEY=... EDOC_FOLDER_ID=204100 \
//	go test ./internal/datasource/connector/edoc/ -run TestRealEDocReadOnly -v
//
// EDOC_MAX_FILES caps how many files are downloaded (default 2) so the test
// stays cheap against large folders.
func TestRealEDocReadOnly(t *testing.T) {
	baseURL := os.Getenv("EDOC_BASE_URL")
	loginName := os.Getenv("EDOC_LOGIN_NAME")
	integrationKey := os.Getenv("EDOC_INTEGRATION_KEY")
	folderID := os.Getenv("EDOC_FOLDER_ID")
	if baseURL == "" || loginName == "" || integrationKey == "" || folderID == "" {
		t.Skip("set EDOC_BASE_URL, EDOC_LOGIN_NAME, EDOC_INTEGRATION_KEY and EDOC_FOLDER_ID to run")
	}
	maxFiles := 2
	if raw := os.Getenv("EDOC_MAX_FILES"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			maxFiles = n
		}
	}

	cfg, err := parseConfig(&types.DataSourceConfig{
		Type: types.ConnectorTypeEDoc,
		Credentials: map[string]interface{}{
			"base_url":        baseURL,
			"login_name":      loginName,
			"integration_key": integrationKey,
		},
	})
	require.NoError(t, err)

	ctx := context.Background()
	client := newClient(cfg)
	token, err := client.login(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	id, err := strconv.ParseInt(folderID, 10, 64)
	require.NoError(t, err)
	info, err := client.folderInfo(ctx, id)
	require.NoError(t, err)
	t.Logf("folder %d %q: %d folders, %d files", info.ID, info.Name, info.ChildFolderCount, info.ChildFileCount)

	files, err := client.listFiles(ctx, id)
	require.NoError(t, err)
	require.NotEmpty(t, files)

	downloaded := 0
	for _, file := range files {
		if downloaded >= maxFiles {
			break
		}
		if file.size() == 0 {
			continue
		}
		check, err := client.downloadCheck(ctx, file.ID)
		if err != nil {
			// A least-privilege account may only be allowed to download some
			// folders; report and continue instead of failing the listing check.
			t.Logf("skip %d %q: %v", file.ID, file.Name, err)
			require.ErrorIs(t, err, datasource.ErrFetchFailed)
			continue
		}
		body, err := client.download(ctx, check.RegionHash, file.size()+1)
		require.NoError(t, err)
		require.Equal(t, file.size(), int64(len(body)), "downloaded size must match the listing")
		t.Logf("downloaded %d %q: %d bytes", file.ID, file.Name, len(body))
		downloaded++
	}
	require.Positive(t, downloaded, "at least one file must be downloadable for this account")
}