package service

import (
	"context"
	"sync/atomic"

	"github.com/Tencent/WeKnora/internal/types"
)

// previewEnqueuerFn starts the derived-PDF render for one freshly ingested
// document. It is installed by the container and is not request-scoped: every
// ingestion path (HTTP upload, data source sync, IM direct upload, MCP ingest,
// passages) funnels through the knowledge service, which calls this trigger.
type previewEnqueuerFn func(context.Context, *types.Knowledge)

var previewEnqueuer atomic.Pointer[previewEnqueuerFn]

// SetKnowledgePreviewEnqueuer installs the process-wide preview trigger.
// Passing nil clears it (tests).
func SetKnowledgePreviewEnqueuer(fn func(context.Context, *types.Knowledge)) {
	if fn == nil {
		previewEnqueuer.Store(nil)
		return
	}
	holder := previewEnqueuerFn(fn)
	previewEnqueuer.Store(&holder)
}

// enqueueKnowledgePreview asks the preview service to prepare the derived PDF.
// Ensure only marks the row pending and starts a bounded background render, so
// it never blocks the ingestion call.
func enqueueKnowledgePreview(ctx context.Context, knowledge *types.Knowledge) {
	fn := previewEnqueuer.Load()
	if fn == nil || knowledge == nil {
		return
	}
	(*fn)(context.WithoutCancel(ctx), knowledge)
}
