package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// PreviewArtifactRepository stores derived PDF previews.
type PreviewArtifactRepository interface {
	// Get returns the artifact row or nil when the knowledge item has none.
	Get(ctx context.Context, tenantID uint64, knowledgeID string) (*types.KnowledgePreviewArtifact, error)
	// Upsert inserts or replaces the row for one knowledge item.
	Upsert(ctx context.Context, artifact *types.KnowledgePreviewArtifact) error
	// Delete removes the row for one knowledge item.
	Delete(ctx context.Context, tenantID uint64, knowledgeID string) error
}

// DocumentPDFRenderer is the optional docreader capability that converts an
// Office document to PDF. It is a separate interface from DocumentReader so
// transports without LibreOffice still satisfy the parser contract.
type DocumentPDFRenderer interface {
	RenderToPDF(ctx context.Context, content []byte, fileName, fileType string) ([]byte, int32, error)
}

// PreviewArtifactService produces and serves derived PDF previews.
type PreviewArtifactService interface {
	// Get returns the current artifact without starting a render.
	Get(ctx context.Context, knowledge *types.Knowledge) (*types.KnowledgePreviewArtifact, error)
	// Ensure returns a ready artifact, or starts a render and returns the
	// pending row. A failed artifact is returned as-is: only an explicit
	// Retry (a user pressing the retry button) starts a new conversion, so
	// polling the preview state never re-runs LibreOffice.
	Ensure(ctx context.Context, knowledge *types.Knowledge) (*types.KnowledgePreviewArtifact, error)
	// Retry forces a new render even when the artifact previously failed.
	Retry(ctx context.Context, knowledge *types.Knowledge) (*types.KnowledgePreviewArtifact, error)
	// Delete drops the artifact row and its stored PDF.
	Delete(ctx context.Context, knowledge *types.Knowledge) error
}
