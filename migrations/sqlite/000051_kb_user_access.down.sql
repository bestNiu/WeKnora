-- Rollback: 000051_kb_user_access
DROP INDEX IF EXISTS idx_kb_user_access_deleted_at;
DROP INDEX IF EXISTS idx_kb_user_access_kb;
DROP INDEX IF EXISTS idx_kb_user_access_tenant_user;
DROP INDEX IF EXISTS idx_kb_user_access_kb_user;
DROP TABLE IF EXISTS kb_user_access;

ALTER TABLE embed_channels  DROP COLUMN owner_user_id;
ALTER TABLE im_channels     DROP COLUMN owner_user_id;
ALTER TABLE tenant_api_keys DROP COLUMN owner_user_id;
ALTER TABLE knowledge_bases DROP COLUMN visibility_mode;