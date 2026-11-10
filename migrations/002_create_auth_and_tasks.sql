CREATE TABLE app.users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    password_hash text NOT NULL,
    status text NOT NULL DEFAULT 'ACTIVE',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT users_email_normalized CHECK (
        email = lower(btrim(email))
        AND char_length(email) BETWEEN 3 AND 254
    ),
    CONSTRAINT users_email_unique UNIQUE (email),
    CONSTRAINT users_password_hash_size CHECK (char_length(password_hash) BETWEEN 1 AND 512),
    CONSTRAINT users_status_valid CHECK (status IN ('ACTIVE', 'DISABLED'))
);

CREATE TABLE app.sessions (
    token_hash bytea PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    last_activity_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    idle_expires_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at timestamptz NULL,
    CONSTRAINT sessions_token_hash_size CHECK (octet_length(token_hash) = 32),
    CONSTRAINT sessions_expiry_order CHECK (
        idle_expires_at > created_at
        AND absolute_expires_at > created_at
        AND idle_expires_at <= absolute_expires_at
    ),
    CONSTRAINT sessions_activity_after_creation CHECK (last_activity_at >= created_at),
    CONSTRAINT sessions_revocation_after_creation CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

CREATE INDEX sessions_user_expiry_idx ON app.sessions (user_id, absolute_expires_at);
CREATE INDEX sessions_active_expiry_idx ON app.sessions (idle_expires_at, absolute_expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE app.tasks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
    title varchar(200) NOT NULL,
    description text NULL,
    status text NOT NULL DEFAULT 'TODO',
    completed_at timestamptz NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    deleted_at timestamptz NULL,
    CONSTRAINT tasks_owner_id_unique UNIQUE (id, user_id),
    CONSTRAINT tasks_title_valid CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT tasks_description_size CHECK (description IS NULL OR char_length(description) <= 10000),
    CONSTRAINT tasks_status_valid CHECK (status IN ('TODO', 'IN_PROGRESS', 'DONE')),
    CONSTRAINT tasks_completed_at_consistent CHECK ((status = 'DONE') = (completed_at IS NOT NULL)),
    CONSTRAINT tasks_version_positive CHECK (version > 0)
);

CREATE INDEX tasks_owner_created_idx ON app.tasks (user_id, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
CREATE INDEX tasks_owner_status_created_idx ON app.tasks (user_id, status, created_at DESC, id DESC)
    WHERE deleted_at IS NULL;
