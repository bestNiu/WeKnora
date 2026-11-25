-- Migration: 000139_kb_member_tags
-- Operator-assigned labels on a knowledge base's members ("医学事务", "BD",
-- "外部CRO" …). They exist to make a large member list operable: filter by
-- label, then grant/revoke in bulk.
--
-- A label is per (KB, member, tag) — the same person can carry different labels
-- on different knowledge bases, which matches how access is granted.
CREATE TABLE IF NOT EXISTS kb_member_tags (
    id                VARCHAR(36) NOT NULL PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    user_id           VARCHAR(36) NOT NULL,
    tag               VARCHAR(64) NOT NULL,
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_member_tags_unique
    ON kb_member_tags (knowledge_base_id, user_id, tag);
CREATE INDEX IF NOT EXISTS idx_kb_member_tags_tag
    ON kb_member_tags (knowledge_base_id, tag);
CREATE INDEX IF NOT EXISTS idx_kb_member_tags_user
    ON kb_member_tags (tenant_id, user_id);
