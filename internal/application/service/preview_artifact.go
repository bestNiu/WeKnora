package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// maxPreviewSourceBytes matches the docreader cap. Larger sources are
	// marked failed instead of buffering hundreds of megabytes in the app.
	maxPreviewSourceBytes = 150 * 1024 * 1024
	// stalePendingAfter lets a crashed render be retried without blocking the
	// preview forever.
	stalePendingAfter = 10 * time.Minute
	// previewRenderConcurrency bounds LibreOffice: one conversion peaks around
	// 1 GB RSS. Operators can raise it for a bulk backfill through
	// PREVIEW_RENDER_CONCURRENCY (capped at 8).
	previewRenderConcurrency = 2
)

// previewArtifactService renders Office documents to PDF once and caches the
// result.
//
// Generation is asynchronous: a preview open marks the row pending and returns
// immediately, so a 56 MB deck never holds an HTTP request for a minute. The
// row is the coordination point, and this process keeps a small in-flight set
// so repeated opens do not spawn duplicate conversions.
type previewArtifactService struct {
	repo     interfaces.PreviewArtifactRepository
	kg       interfaces.KnowledgeService
	files    interfaces.FileService
	renderer interfaces.DocumentPDFRenderer

	sem      chan struct{}
	mu       sync.Mutex
	inflight map[string]struct{}
}

// NewPreviewArtifactService builds the service. reader is the docreader client;
// when it does not implement DocumentPDFRenderer the service degrades to "no
// preview" and the handler serves the original bytes.
func NewPreviewArtifactService(
	repo interfaces.PreviewArtifactRepository,
	kg interfaces.KnowledgeService,
	files interfaces.FileService,
	reader interfaces.DocumentReader,
) interfaces.PreviewArtifactService {
	renderer, _ := reader.(interfaces.DocumentPDFRenderer)
	concurrency := previewRenderConcurrency
	if raw := strings.TrimSpace(os.Getenv("PREVIEW_RENDER_CONCURRENCY")); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil && n > 0 && n <= 8 {
			concurrency = n
		}
	}
	return &previewArtifactService{
		repo:     repo,
		kg:       kg,
		files:    files,
		renderer: renderer,
		sem:      make(chan struct{}, concurrency),
		inflight: make(map[string]struct{}),
	}
}

// Get returns the current artifact without starting a render.
func (s *previewArtifactService) Get(
	ctx context.Context, knowledge *types.Knowledge,
) (*types.KnowledgePreviewArtifact, error) {
	if s == nil || s.repo == nil || knowledge == nil {
		return nil, nil
	}
	return s.repo.Get(ctx, knowledge.TenantID, knowledge.ID)
}

// Ensure returns a ready artifact, or starts a render and returns the pending
// row. A failed artifact is returned as-is: only an explicit Retry (the user's
// retry button) starts a new conversion, so polling the state never re-runs
// LibreOffice.
func (s *previewArtifactService) Ensure(
	ctx context.Context, knowledge *types.Knowledge,
) (*types.KnowledgePreviewArtifact, error) {
	return s.ensure(ctx, knowledge, false)
}

// Retry forces a new render even when the artifact previously failed.
func (s *previewArtifactService) Retry(
	ctx context.Context, knowledge *types.Knowledge,
) (*types.KnowledgePreviewArtifact, error) {
	return s.ensure(ctx, knowledge, true)
}

func (s *previewArtifactService) ensure(
	ctx context.Context, knowledge *types.Knowledge, retryFailed bool,
) (*types.KnowledgePreviewArtifact, error) {
	if s == nil || s.repo == nil || knowledge == nil {
		return nil, errors.New("preview artifact service is unavailable")
	}
	if !types.IsRenderablePreviewType(knowledge.FileType) {
		return nil, nil
	}
	if s.renderer == nil || s.files == nil || s.kg == nil {
		return &types.KnowledgePreviewArtifact{
			KnowledgeID: knowledge.ID, TenantID: knowledge.TenantID,
			Status: types.PreviewArtifactFailed, ErrorMessage: "PDF renderer is not available",
		}, nil
	}

	existing, err := s.repo.Get(ctx, knowledge.TenantID, knowledge.ID)
	if err != nil {
		return nil, err
	}
	if existing.IsReady() {
		return existing, nil
	}
	if existing != nil && existing.Status == types.PreviewArtifactPending &&
		time.Since(existing.UpdatedAt) < stalePendingAfter {
		return existing, nil
	}
	if existing != nil && existing.Status == types.PreviewArtifactFailed && !retryFailed {
		return existing, nil
	}

	pending := &types.KnowledgePreviewArtifact{
		KnowledgeID: knowledge.ID,
		TenantID:    knowledge.TenantID,
		Status:      types.PreviewArtifactPending,
	}
	if existing != nil {
		pending.CreatedAt = existing.CreatedAt
	}
	if err := s.repo.Upsert(ctx, pending); err != nil {
		return nil, err
	}

	s.startRender(knowledge)
	return pending, nil
}

// startRender launches one conversion per knowledge item per process.
func (s *previewArtifactService) startRender(knowledge *types.Knowledge) {
	key := fmt.Sprintf("%d:%s", knowledge.TenantID, knowledge.ID)
	s.mu.Lock()
	if _, running := s.inflight[key]; running {
		s.mu.Unlock()
		return
	}
	s.inflight[key] = struct{}{}
	s.mu.Unlock()

	// The render outlives the request that triggered it.
	base := context.Background()
	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()
		s.sem <- struct{}{}
		defer func() { <-s.sem }()
		if err := s.render(base, knowledge); err != nil {
			logger.ErrorWithFields(base, err, map[string]interface{}{
				"knowledge_id": knowledge.ID, "tenant_id": knowledge.TenantID,
			})
		}
	}()
}

// render converts, stores and records one artifact.
func (s *previewArtifactService) render(ctx context.Context, knowledge *types.Knowledge) error {
	ctx = types.WithExecutionTenant(ctx, knowledge.TenantID)
	ctx = types.WithCaller(ctx, types.Caller{TenantID: knowledge.TenantID})

	fail := func(cause error) error {
		message := cause.Error()
		if len(message) > 500 {
			message = message[:500]
		}
		_ = s.repo.Upsert(context.Background(), &types.KnowledgePreviewArtifact{
			KnowledgeID: knowledge.ID, TenantID: knowledge.TenantID,
			Status: types.PreviewArtifactFailed, ErrorMessage: message,
		})
		return cause
	}

	file, filename, err := s.kg.GetKnowledgeFile(ctx, knowledge.ID)
	if err != nil {
		return fail(fmt.Errorf("read source file: %w", err))
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, maxPreviewSourceBytes+1))
	if err != nil {
		return fail(fmt.Errorf("read source bytes: %w", err))
	}
	if len(content) > maxPreviewSourceBytes {
		return fail(fmt.Errorf("source file exceeds the %d MB render cap", maxPreviewSourceBytes/(1024*1024)))
	}

	pdf, pageCount, err := s.renderer.RenderToPDF(ctx, content, filename, knowledge.FileType)
	if err != nil {
		return fail(fmt.Errorf("render PDF: %w", err))
	}

	baseName := strings.TrimSuffix(filename, "."+knowledge.FileType)
	storagePath, err := s.files.SaveBytes(
		ctx, pdf, knowledge.TenantID, baseName+".pdf", false,
	)
	if err != nil {
		return fail(fmt.Errorf("store preview PDF: %w", err))
	}

	ready := &types.KnowledgePreviewArtifact{
		KnowledgeID: knowledge.ID, TenantID: knowledge.TenantID,
		Status: types.PreviewArtifactReady, StoragePath: storagePath,
		PageCount: int(pageCount),
	}
	if err := s.repo.Upsert(ctx, ready); err != nil {
		return fmt.Errorf("record preview artifact: %w", err)
	}
	logger.Infof(ctx, "Preview PDF ready for knowledge %s (%d pages)", knowledge.ID, pageCount)
	return nil
}

// Backfill queues a render for every renderable document in the workspace.
// It reuses Ensure, so already-rendered rows are skipped and a failed row is
// retried; the artifact table is the progress record.
func (s *previewArtifactService) Backfill(
	ctx context.Context, kbs []*types.KnowledgeBase, knowledge []*types.Knowledge,
) (queued int, err error) {
	_ = kbs
	for _, knowledgeItem := range knowledge {
		if knowledgeItem == nil || !types.IsRenderablePreviewType(knowledgeItem.FileType) {
			continue
		}
		if _, ensureErr := s.Ensure(ctx, knowledgeItem); ensureErr != nil {
			logger.Warnf(ctx, "Preview backfill failed to queue %s: %v", knowledgeItem.ID, ensureErr)
			continue
		}
		queued++
	}
	return queued, nil
}

// Stats returns the artifact status distribution for one tenant.
func (s *previewArtifactService) Stats(
	ctx context.Context, tenantID uint64,
) (map[string]int64, error) {
	repo, ok := s.repo.(interface {
		CountByStatus(context.Context, uint64) (map[string]int64, error)
	})
	if !ok {
		return nil, errors.New("preview artifact repository cannot report status")
	}
	return repo.CountByStatus(ctx, tenantID)
}

// Delete drops the artifact row and its stored PDF.
func (s *previewArtifactService) Delete(
	ctx context.Context, knowledge *types.Knowledge,
) error {
	if s == nil || s.repo == nil || knowledge == nil {
		return nil
	}
	artifact, err := s.repo.Get(ctx, knowledge.TenantID, knowledge.ID)
	if err != nil || artifact == nil {
		return err
	}
	if artifact.StoragePath != "" && s.files != nil {
		if delErr := s.files.DeleteFile(ctx, artifact.StoragePath); delErr != nil {
			logger.Warnf(ctx, "Failed to delete preview PDF %s: %v", artifact.StoragePath, delErr)
		}
	}
	return s.repo.Delete(ctx, knowledge.TenantID, knowledge.ID)
}
