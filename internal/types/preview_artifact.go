package types

import "time"

// PreviewArtifactStatus is the lifecycle of a derived PDF preview.
type PreviewArtifactStatus string

const (
	// PreviewArtifactPending means a render is queued or running.
	PreviewArtifactPending PreviewArtifactStatus = "pending"
	// PreviewArtifactReady means StoragePath holds the PDF.
	PreviewArtifactReady PreviewArtifactStatus = "ready"
	// PreviewArtifactFailed keeps the reason for the fallback chain.
	PreviewArtifactFailed PreviewArtifactStatus = "failed"
)

// KnowledgePreviewArtifact is the derived PDF preview for one knowledge item.
//
// The row doubles as render job state: a pending row without a running render
// is stale and may be retried, and concurrent preview opens reuse it instead of
// starting a second LibreOffice conversion.
type KnowledgePreviewArtifact struct {
	KnowledgeID  string                `json:"knowledge_id" gorm:"type:varchar(36);primaryKey"`
	TenantID     uint64                `json:"tenant_id"    gorm:"not null;index:idx_preview_artifacts_tenant_status"`
	Status       PreviewArtifactStatus `json:"status"       gorm:"type:varchar(16);not null;default:'pending';index:idx_preview_artifacts_tenant_status"`
	StoragePath  string                `json:"-"            gorm:"type:text;not null;default:''"`
	PageCount    int                   `json:"page_count"   gorm:"not null;default:0"`
	ContentHash  string                `json:"-"            gorm:"type:varchar(64);not null;default:''"`
	ErrorMessage string                `json:"error,omitempty" gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// TableName returns the GORM table name.
func (KnowledgePreviewArtifact) TableName() string {
	return "knowledge_preview_artifacts"
}

// IsReady reports whether the artifact can be served.
func (a *KnowledgePreviewArtifact) IsReady() bool {
	return a != nil && a.Status == PreviewArtifactReady && a.StoragePath != ""
}

// renderablePreviewTypes lists the extensions LibreOffice can turn into a PDF
// preview. Everything else is its own preview (text, markdown, images, audio,
// video), so the original bytes are the preview.
var renderablePreviewTypes = map[string]struct{}{
	"ppt": {}, "pptx": {}, "doc": {}, "docx": {}, "xls": {}, "xlsx": {},
	"odp": {}, "odt": {}, "ods": {},
	// A PDF is a source document too: its preview is a rasterised image-only
	// copy, so /preview never returns the original upload.
	"pdf": {},
}

// IsRenderablePreviewType reports whether fileType needs a derived PDF.
func IsRenderablePreviewType(fileType string) bool {
	normalized := ""
	for _, r := range fileType {
		switch {
		case r >= 'A' && r <= 'Z':
			normalized += string(r + 32)
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			normalized += string(r)
		case r == '.':
			// A leading dot is harmless; an inner dot is not part of a type.
		}
	}
	_, ok := renderablePreviewTypes[normalized]
	return ok
}
