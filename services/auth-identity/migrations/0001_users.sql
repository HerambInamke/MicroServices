-- IF NOT EXISTS: databases created before versioned migrations already have this table.
CREATE TABLE IF NOT EXISTS users (
	id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	email          text NOT NULL UNIQUE,
	phone          text UNIQUE,
	password_hash  text NOT NULL,
	role           text NOT NULL,
	wallet_address text UNIQUE,
	created_at     timestamptz NOT NULL DEFAULT now()
);
