-- Rollback: 000054_user_identity (SQLite)
DROP INDEX IF EXISTS idx_user_identities_user;
DROP TABLE IF EXISTS user_identities;
