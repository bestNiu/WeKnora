-- Migration: 000135_knowledge_preview_artifacts
-- Derived PDF previews for Office documents.
--
-- The preview surface must never ship the original upload: that file is the
-- asset a knowledge-base download permission protects. Each knowledge row gets
-- at most one artifact here. The row doubles as the job state so concurrent
-- opens do not start the same LibreOffice conversion twice.

DO $$ BEGIN RAISE NOTICE '[Migration 000135] Creating table: knowledge_preview_artifacts'; END $$;

CREATE TABLE IF NOT EXISTS knowledge_preview_artifacts (
    knowledge_id  VARCHAR(36) PRIMARY KEY,
    tenant_id     BIGINT      NOT NULL,
    status        VARCHAR(16) NOT NULL DEFAULT 'pending',
    storage_path  TEXT        NOT NULL DEFAULT '',
    page_count    INTEGER     NOT NULL DEFAULT 0,
    content_hash  VARCHAR(64) NOT NULL DEFAULT '',
    error_message TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE knowledge_preview_artifacts
    DROP CONSTRAINT IF EXISTS chk_preview_artifact_status;
ALTER TABLE knowledge_preview_artifacts
    ADD CONSTRAINT chk_preview_artifact_status
    CHECK (status IN ('pending', 'ready', 'failed'));

CREATE INDEX IF NOT EXISTS idx_preview_artifacts_tenant_status
    ON knowledge_preview_artifacts (tenant_id, status);

ALTER TABLE knowledge_preview_artifacts
    DROP CONSTRAINT IF EXISTS knowledge_preview_artifacts_knowledge_id_fkey;
ALTER TABLE knowledge_preview_artifacts
    ADD CONSTRAINT knowledge_preview_artifacts_knowledge_id_fkey
    FOREIGN KEY (knowledge_id) REFERENCES knowledges(id) ON DELETE CASCADE;

DO $$ BEGIN RAISE NOTICE '[Migration 000135] knowledge_preview_artifacts ready'; END $$;