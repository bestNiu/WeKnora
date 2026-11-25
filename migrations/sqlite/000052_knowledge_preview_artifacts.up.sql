-- Migration: 000052_knowledge_preview_artifacts (sqlite mirror of versioned 000135)
-- Derived PDF previews for Office documents; one row per knowledge item.
CREATE TABLE IF NOT EXISTS knowledge_preview_artifacts (
    knowledge_id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    storage_path TEXT NOT NULL DEFAULT '',
    page_count INTEGER NOT NULL DEFAULT 0,
    content_hash VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (status IN ('pending', 'ready', 'failed'))
);
CREATE INDEX IF NOT EXISTS idx_preview_artifacts_tenant_status
    ON knowledge_preview_artifacts (tenant_id, status);
