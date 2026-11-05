-- Rollback: 000053_agent_access
DROP INDEX IF EXISTS idx_agent_user_access_deleted_at;
DROP INDEX IF EXISTS idx_agent_user_access_agent;
DROP INDEX IF EXISTS idx_agent_user_access_tenant_user;
DROP INDEX IF EXISTS idx_agent_user_access_agent_user;
DROP TABLE IF EXISTS agent_user_access;
DROP TABLE IF EXISTS agent_access_policy;