-- IF NOT EXISTS: databases created before versioned migrations already have this table.
CREATE TABLE IF NOT EXISTS refresh_tokens (
	jti        text PRIMARY KEY,
	user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at timestamptz NOT NULL,
	revoked    boolean NOT NULL DEFAULT false
);
