-- Migration: 000134_kb_user_access
-- Per-user access overrides for a knowledge base, plus the machine-principal
-- owner binding that lets API keys, IM channels and embed channels inherit a
-- user's access decisions.
--
-- visibility_mode
--   'tenant'     the whole workspace can read the KB (default, current behaviour)
--   'restricted' only kb_user_access.can_view rows, the KB creator and the
--                workspace Owner/Admin can read it
--
-- Download is decided independently of visibility: Owner/Admin/creator, or an
-- explicit can_download grant. A tenant Contributor no longer implies download.
--
-- owner_user_id
--   The user a machine principal acts as. NULL (legacy rows) means "no bound
--   user": restricted KBs stay invisible and nothing is downloadable.

DO $$ BEGIN RAISE NOTICE '[Migration 000134] knowledge_bases.visibility_mode'; END $$;

ALTER TABLE knowledge_bases
    ADD COLUMN IF NOT EXISTS visibility_mode VARCHAR(16) NOT NULL DEFAULT 'tenant';

ALTER TABLE knowledge_bases
    DROP CONSTRAINT IF EXISTS chk_knowledge_bases_visibility_mode;
ALTER TABLE knowledge_bases
    ADD CONSTRAINT chk_knowledge_bases_visibility_mode
    CHECK (visibility_mode IN ('tenant', 'restricted'));

DO $$ BEGIN RAISE NOTICE '[Migration 000134] machine principal owner_user_id'; END $$;

ALTER TABLE tenant_api_keys ADD COLUMN IF NOT EXISTS owner_user_id VARCHAR(36);
ALTER TABLE im_channels     ADD COLUMN IF NOT EXISTS owner_user_id VARCHAR(36);
ALTER TABLE embed_channels  ADD COLUMN IF NOT EXISTS owner_user_id VARCHAR(36);

DO $$ BEGIN RAISE NOTICE '[Migration 000134] Creating table: kb_user_access'; END $$;

CREATE TABLE IF NOT EXISTS kb_user_access (
    id                VARCHAR(36) PRIMARY KEY,
    tenant_id         BIGINT      NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    user_id           VARCHAR(36) NOT NULL,
    can_view          BOOLEAN     NOT NULL DEFAULT TRUE,
    can_download      BOOLEAN     NOT NULL DEFAULT FALSE,
    granted_by        VARCHAR(36),
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP WITH TIME ZONE
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

ALTER TABLE kb_user_access
    DROP CONSTRAINT IF EXISTS kb_user_access_knowledge_base_id_fkey;
ALTER TABLE kb_user_access
    ADD CONSTRAINT kb_user_access_knowledge_base_id_fkey
    FOREIGN KEY (knowledge_base_id) REFERENCES knowledge_bases(id) ON DELETE CASCADE;

DO $$ BEGIN RAISE NOTICE '[Migration 000134] kb_user_access ready'; END $$;