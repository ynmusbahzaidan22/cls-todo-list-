CREATE TABLE app.auth_rate_limits (
    key_hash bytea PRIMARY KEY,
    window_started_at timestamptz NOT NULL,
    attempt_count integer NOT NULL CHECK (attempt_count > 0),
    expires_at timestamptz NOT NULL,
    CONSTRAINT auth_rate_limits_key_hash_size CHECK (octet_length(key_hash) = 32)
);

CREATE INDEX auth_rate_limits_expiry_idx ON app.auth_rate_limits (expires_at);

---- create above / drop below ----

DROP TABLE app.auth_rate_limits;
