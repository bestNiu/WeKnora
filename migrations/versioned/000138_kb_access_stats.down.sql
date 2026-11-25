DO $$ BEGIN RAISE NOTICE '[Migration 000138 down] Dropping table: kb_access_stats'; END $$;
DROP TABLE IF EXISTS kb_access_stats;
