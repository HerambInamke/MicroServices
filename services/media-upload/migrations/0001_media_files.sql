-- IF NOT EXISTS: databases created before versioned migrations already have this table.
CREATE TABLE IF NOT EXISTS media_files (
	id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	owner_id     text NOT NULL,
	object_key   text NOT NULL UNIQUE,
	content_type text NOT NULL,
	size_bytes   bigint NOT NULL,
	kind         text NOT NULL,
	uploaded_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_media_owner ON media_files (owner_id);
