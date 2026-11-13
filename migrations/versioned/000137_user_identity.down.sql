-- Rollback: 000137_user_identity
DO $$ BEGIN RAISE NOTICE '[Migration 000137 down] Dropping table: user_identities'; END $$;

DROP INDEX IF EXISTS idx_user_identities_user;
DROP TABLE IF EXISTS user_identities;
