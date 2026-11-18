-- Migration: 000138_kb_access_stats
-- Per-member access telemetry for a knowledge base, plus the risk state the
-- download guard reads and writes.
--
-- Why a table instead of counting audit_logs: the member list has to show
-- views/downloads and the download guard has to count "today" and "last ten
-- minutes" on every request. Aggregating the audit stream each time would put
-- a scan in the hot path; audit_logs stays the immutable detail trail.
--
-- quota_day / downloads_quota_day reset lazily: the writer compares the stored
-- day with today and starts a new count, so no cron job is needed.

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Creating table: kb_access_stats'; END $$;

CREATE TABLE IF NOT EXISTS kb_access_stats (
    id                       VARCHAR(36) NOT NULL PRIMARY KEY,
    tenant_id                BIGINT      NOT NULL,
    knowledge_base_id        VARCHAR(36) NOT NULL,
    user_id                  VARCHAR(36) NOT NULL,
    views_total              BIGINT      NOT NULL DEFAULT 0,
    downloads_total          BIGINT      NOT NULL DEFAULT 0,
    last_view_at             TIMESTAMP WITH TIME ZONE,
    last_download_at         TIMESTAMP WITH TIME ZONE,
    quota_day                DATE,
    downloads_quota_day      INT         NOT NULL DEFAULT 0,
    window_started_at        TIMESTAMP WITH TIME ZONE,
    downloads_in_window      INT         NOT NULL DEFAULT 0,
    risk_flag                BOOLEAN     NOT NULL DEFAULT FALSE,
    risk_reason              VARCHAR(200) NOT NULL DEFAULT '',
    download_suspended_until TIMESTAMP WITH TIME ZONE,
    created_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_kb_access_stats_kb_user
    ON kb_access_stats (knowledge_base_id, user_id);
CREATE INDEX IF NOT EXISTS idx_kb_access_stats_tenant
    ON kb_access_stats (tenant_id, knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_kb_access_stats_risk
    ON kb_access_stats (tenant_id, knowledge_base_id) WHERE risk_flag;
