-- One QR code per ledger piece. current_owner_id and ledger_ref are a cache of the ledger,
-- kept fresh by the piece.handoff.signed consumer; event_count orders those updates.
CREATE TABLE pieces (
	id               text PRIMARY KEY,
	product_id       bigint,
	qr_code          text NOT NULL UNIQUE,
	current_owner_id text NOT NULL,
	ledger_ref       text NOT NULL DEFAULT '',
	event_count      integer NOT NULL,
	created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE scans (
	id         bigserial PRIMARY KEY,
	qr_code    text NOT NULL REFERENCES pieces(qr_code),
	owner_id   text NOT NULL,
	scanned_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scans_qr_code_idx ON scans (qr_code);
