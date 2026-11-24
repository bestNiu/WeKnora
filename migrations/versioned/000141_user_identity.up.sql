-- Migration: 000137_user_identity
-- External identity bindings (WeCom today).
--
-- Login must not key off email: a renamed mailbox would rebind the account,
-- and a WeCom member may have no mailbox at all. (provider, subject) is the
-- stable key — for WeCom, subject is the member userid.

DO $$ BEGIN RAISE NOTICE '[Migration 000137] Creating table: user_identities'; END $$;

CREATE TABLE IF NOT EXISTS user_identities (
    id         VARCHAR(36)  NOT NULL PRIMARY KEY,
    user_id    VARCHAR(36)  NOT NULL,
    provider   VARCHAR(32)  NOT NULL,
    subject    VARCHAR(128) NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_user_identities_provider_subject UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities (user_id);