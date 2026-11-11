ALTER TABLE app.users
    ADD COLUMN name text;

-- Use a stable, non-personal placeholder for every existing account. This
-- backfill does not depend on account count or disclose/invent personal names.
UPDATE app.users
SET name = 'User ' || left(id::text, 8);

ALTER TABLE app.users
    ALTER COLUMN name SET DEFAULT 'User',
    ALTER COLUMN name SET NOT NULL,
    ADD CONSTRAINT users_name_valid CHECK (
        name = btrim(name, E' \t\n\r')
        AND char_length(name) BETWEEN 1 AND 100
    );

ALTER TABLE app.users
    ADD COLUMN role text NOT NULL DEFAULT 'USER',
    ADD CONSTRAINT users_role_valid CHECK (role IN ('USER', 'ADMIN'));

---- create above / drop below ----

-- This migration is intentionally forward-only. Rolling it back would remove
-- user names and roles that may have been added or changed after deployment.
DO $migration$
BEGIN
    RAISE EXCEPTION 'user name and role migration is forward-only';
END
$migration$;
