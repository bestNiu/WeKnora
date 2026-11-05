-- Rollback: 000134_kb_user_access
-- Drops the per-user KB access overrides and the machine-principal owner
-- binding. visibility_mode returns to its default and is dropped.

DO $$ BEGIN RAISE NOTICE '[Migration 000134] Rollback: dropping kb_user_access'; END $$;

DROP INDEX IF EXISTS idx_kb_user_access_deleted_at;
DROP INDEX IF EXISTS idx_kb_user_access_kb;
DROP INDEX IF EXISTS idx_kb_user_access_tenant_user;
DROP INDEX IF EXISTS idx_kb_user_access_kb_user;
DROP TABLE IF EXISTS kb_user_access;

ALTER TABLE embed_channels  DROP COLUMN IF EXISTS owner_user_id;
ALTER TABLE im_channels     DROP COLUMN IF EXISTS owner_user_id;
ALTER TABLE tenant_api_keys DROP COLUMN IF EXISTS owner_user_id;

ALTER TABLE knowledge_bases DROP CONSTRAINT IF EXISTS chk_knowledge_bases_visibility_mode;
ALTER TABLE knowledge_bases DROP COLUMN IF EXISTS visibility_mode;