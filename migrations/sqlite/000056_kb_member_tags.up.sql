-- Migration: 000056_kb_member_tags (SQLite) — mirrors versioned/000139.
CREATE TABLE IF NOT EXISTS kb_member_tags (
    id                VARCHAR(36) NOT NULL PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    user_id           VARCHAR(36) NOT NULL,
    tag               VARCHAR(64) NOT NULL,
    created_at        DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_member_tags_unique
    ON kb_member_tags (knowledge_base_id, user_id, tag);
CREATE INDEX IF NOT EXISTS idx_kb_member_tags_tag
    ON kb_member_tags (knowledge_base_id, tag);
