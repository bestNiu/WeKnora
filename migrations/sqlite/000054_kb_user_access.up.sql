-- Migration: 000051_kb_user_access (sqlite mirror of versioned 000134)
-- Per-user KB access overrides + machine-principal owner binding.
-- SQLite supports partial unique indexes, so the "one active row per
-- (kb, user)" guard is applied verbatim.

ALTER TABLE knowledge_bases ADD COLUMN visibility_mode VARCHAR(16) NOT NULL DEFAULT 'tenant';
ALTER TABLE tenant_api_keys ADD COLUMN owner_user_id VARCHAR(36);
ALTER TABLE im_channels     ADD COLUMN owner_user_id VARCHAR(36);
ALTER TABLE embed_channels  ADD COLUMN owner_user_id VARCHAR(36);

CREATE TABLE IF NOT EXISTS kb_user_access (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    user_id VARCHAR(36) NOT NULL,
    can_view BOOLEAN NOT NULL DEFAULT 1,
    can_download BOOLEAN NOT NULL DEFAULT 0,
    granted_by VARCHAR(36),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_user_access_kb_user
    ON kb_user_access (knowledge_base_id, user_id)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_kb_user_access_tenant_user
    ON kb_user_access (tenant_id, user_id);
CREATE INDEX IF NOT EXISTS idx_kb_user_access_kb
    ON kb_user_access (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_kb_user_access_deleted_at
    ON kb_user_access (deleted_at);