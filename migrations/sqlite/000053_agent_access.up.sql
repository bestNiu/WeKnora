-- Migration: 000053_agent_access (sqlite mirror of versioned 000136)
-- Per-member agent visibility. SQLite supports partial unique indexes, so the
-- "one live row per (agent, user)" guard is applied verbatim.

CREATE TABLE IF NOT EXISTS agent_access_policy (
    tenant_id       INTEGER NOT NULL,
    agent_id        VARCHAR(36) NOT NULL,
    visibility_mode VARCHAR(16) NOT NULL DEFAULT 'tenant',
    updated_by      VARCHAR(36),
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, agent_id),
    CHECK (visibility_mode IN ('tenant', 'allowlist'))
);

CREATE TABLE IF NOT EXISTS agent_user_access (
    id         VARCHAR(36) PRIMARY KEY,
    tenant_id  INTEGER NOT NULL,
    agent_id   VARCHAR(36) NOT NULL,
    user_id    VARCHAR(36) NOT NULL,
    can_view   BOOLEAN NOT NULL DEFAULT 1,
    granted_by VARCHAR(36),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_user_access_agent_user
    ON agent_user_access (agent_id, user_id)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_agent_user_access_tenant_user
    ON agent_user_access (tenant_id, user_id);
CREATE INDEX IF NOT EXISTS idx_agent_user_access_agent
    ON agent_user_access (agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_user_access_deleted_at
    ON agent_user_access (deleted_at);