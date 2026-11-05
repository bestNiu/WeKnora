package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePreviewRepo struct {
	mu   sync.Mutex
	rows map[string]*types.KnowledgePreviewArtifact
}

func newFakePreviewRepo() *fakePreviewRepo {
	return &fakePreviewRepo{rows: map[string]*types.KnowledgePreviewArtifact{}}
}

func (f *fakePreviewRepo) Get(_ context.Context, tenantID uint64, id string) (*types.KnowledgePreviewArtifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.rows[id]
	if row == nil || row.TenantID != tenantID {
		return nil, nil
	}
	copy := *row
	return &copy, nil
}

func (f *fakePreviewRepo) Upsert(_ context.Context, a *types.KnowledgePreviewArtifact) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	copy := *a
	if copy.UpdatedAt.IsZero() {
		copy.UpdatedAt = time.Now()
	}
	f.rows[a.KnowledgeID] = &copy
	return nil
}

func (f *fakePreviewRepo) Delete(_ context.Context, tenantID uint64, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row := f.rows[id]; row != nil && row.TenantID == tenantID {
		delete(f.rows, id)
	}
	return nil
}

type fakePreviewKG struct {
	interfaces.KnowledgeService
	content []byte
	err     error
}

func (f *fakePreviewKG) GetKnowledgeFile(context.Context, string) (io.ReadCloser, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return io.NopCloser(strings.NewReader(string(f.content))), "deck.pptx", nil
}

type fakePreviewFiles struct {
	interfaces.FileService
	path   string
	saved  []byte
	err    error
	closed bool
}

func (f *fakePreviewFiles) SaveBytes(_ context.Context, data []byte, _ uint64, _ string, _ bool) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	f.saved = data
	return f.path, nil
}

func (f *fakePreviewFiles) GetFile(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("pdf")), nil
}

func (f *fakePreviewFiles) DeleteFile(context.Context, string) error {
	f.closed = true
	return nil
}

type fakeRenderer struct {
	pdf   []byte
	pages int32
	err   error
	calls int
}

func (f *fakeRenderer) RenderToPDF(context.Context, []byte, string, string) ([]byte, int32, error) {
	f.calls++
	return f.pdf, f.pages, f.err
}

func waitReady(t *testing.T, svc interfaces.PreviewArtifactService, knowledge *types.Knowledge) *types.KnowledgePreviewArtifact {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		artifact, err := svc.Get(context.Background(), knowledge)
		require.NoError(t, err)
		if artifact != nil && artifact.IsReady() {
			return artifact
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("preview artifact did not become ready")
	return nil
}

func TestPreviewArtifactServiceRendersOnceAndCaches(t *testing.T) {
	repo := newFakePreviewRepo()
	renderer := &fakeRenderer{pdf: []byte("%PDF"), pages: 53}
	svc := &previewArtifactService{
		repo: repo, kg: &fakePreviewKG{content: []byte("pptx")},
		files: &fakePreviewFiles{path: "local://7/k1.pdf"}, renderer: renderer,
		sem: make(chan struct{}, 2), inflight: map[string]struct{}{},
	}
	knowledge := &types.Knowledge{ID: "k1", TenantID: 7, FileType: "pptx"}

	pending, err := svc.Ensure(context.Background(), knowledge)
	require.NoError(t, err)
	require.Equal(t, types.PreviewArtifactPending, pending.Status)

	ready := waitReady(t, svc, knowledge)
	assert.Equal(t, 53, ready.PageCount)
	assert.Equal(t, "local://7/k1.pdf", ready.StoragePath)
	assert.Equal(t, 1, renderer.calls)

	again, err := svc.Ensure(context.Background(), knowledge)
	require.NoError(t, err)
	assert.True(t, again.IsReady())
	assert.Equal(t, 1, renderer.calls, "a cached artifact is never re-rendered")
}

func TestPreviewArtifactServiceSkipsNonRenderableAndReportsFailures(t *testing.T) {
	renderer := &fakeRenderer{err: errors.New("boom")}
	svc := &previewArtifactService{
		repo: newFakePreviewRepo(), kg: &fakePreviewKG{content: []byte("x")},
		files: &fakePreviewFiles{path: "local://7/k1.pdf"}, renderer: renderer,
		sem: make(chan struct{}, 1), inflight: map[string]struct{}{},
	}

	text := &types.Knowledge{ID: "k2", TenantID: 7, FileType: "md"}
	artifact, err := svc.Ensure(context.Background(), text)
	require.NoError(t, err)
	assert.Nil(t, artifact, "text previews are their own artifact")

	failing := &types.Knowledge{ID: "k3", TenantID: 7, FileType: "pptx"}
	_, err = svc.Ensure(context.Background(), failing)
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		row, _ := svc.Get(context.Background(), failing)
		if row != nil && row.Status == types.PreviewArtifactFailed {
			assert.Contains(t, row.ErrorMessage, "boom")
			// Polling must not restart the conversion; only Retry does.
			_, ensureErr := svc.Ensure(context.Background(), failing)
			require.NoError(t, ensureErr)
			assert.Equal(t, 1, renderer.calls)
			_, _ = svc.Retry(context.Background(), failing)
			deadline2 := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline2) {
				if renderer.calls >= 2 {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("Retry did not start a new render (calls=%d)", renderer.calls)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("failed render was not recorded")
}
