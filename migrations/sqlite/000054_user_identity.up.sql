-- Migration: 000054_user_identity (SQLite)
-- Mirrors migrations/versioned/000137: external identity bindings keyed by
-- (provider, subject) instead of email.
CREATE TABLE IF NOT EXISTS user_identities (
    id         VARCHAR(36)  NOT NULL PRIMARY KEY,
    user_id    VARCHAR(36)  NOT NULL,
    provider   VARCHAR(32)  NOT NULL,
    subject    VARCHAR(128) NOT NULL,
    created_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_user_identities_provider_subject UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities (user_id);
