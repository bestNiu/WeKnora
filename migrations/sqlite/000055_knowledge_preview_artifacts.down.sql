-- Rollback: 000052_knowledge_preview_artifacts
DROP INDEX IF EXISTS idx_preview_artifacts_tenant_status;
DROP TABLE IF EXISTS knowledge_preview_artifacts;
