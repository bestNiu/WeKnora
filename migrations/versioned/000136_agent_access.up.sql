-- Migration: 000136_agent_access
-- Per-member visibility for agents.
--
-- Default is tenant-wide: every workspace member sees the agent, which is the
-- behaviour that predates this table. An "allowlist" policy narrows the agent
-- to agent_user_access rows with can_view, its creator and workspace
-- Owner/Admin.
--
-- The mode lives in its own table rather than on custom_agents so a built-in
-- agent (which may have no custom_agents row until it is customised) can be
-- restricted too, and so a policy write never touches agent config.

DO $$ BEGIN RAISE NOTICE '[Migration 000136] Creating table: agent_access_policy'; END $$;

CREATE TABLE IF NOT EXISTS agent_access_policy (
    tenant_id       BIGINT      NOT NULL,
    agent_id        VARCHAR(36) NOT NULL,
    visibility_mode VARCHAR(16) NOT NULL DEFAULT 'tenant',
    updated_by      VARCHAR(36),
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, agent_id)
);

ALTER TABLE agent_access_policy
    DROP CONSTRAINT IF EXISTS chk_agent_access_policy_mode;
ALTER TABLE agent_access_policy
    ADD CONSTRAINT chk_agent_access_policy_mode
    CHECK (visibility_mode IN ('tenant', 'allowlist'));

DO $$ BEGIN RAISE NOTICE '[Migration 000136] Creating table: agent_user_access'; END $$;

CREATE TABLE IF NOT EXISTS agent_user_access (
    id         VARCHAR(36) PRIMARY KEY,
    tenant_id  BIGINT      NOT NULL,
    agent_id   VARCHAR(36) NOT NULL,
    user_id    VARCHAR(36) NOT NULL,
    can_view   BOOLEAN     NOT NULL DEFAULT TRUE,
    granted_by VARCHAR(36),
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP WITH TIME ZONE
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

DO $$ BEGIN RAISE NOTICE '[Migration 000136] agent_access_policy and agent_user_access ready'; END $$;