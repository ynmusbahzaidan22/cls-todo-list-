CREATE TABLE app.admin_audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_user_id uuid NOT NULL REFERENCES app.users(id) ON DELETE RESTRICT,
    target_user_id uuid NOT NULL REFERENCES app.users(id) ON DELETE RESTRICT,
    action text NOT NULL CHECK (action IN ('USER_ROLE_CHANGED', 'USER_STATUS_CHANGED', 'USER_SESSIONS_REVOKED')),
    before_state jsonb NOT NULL,
    after_state jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT admin_audit_states_are_objects CHECK (jsonb_typeof(before_state) = 'object' AND jsonb_typeof(after_state) = 'object')
);

-- A single transactional counter makes the last-active-Admin invariant safe
-- under concurrent role/status changes, including changes outside the API.
CREATE TABLE app.active_admin_guard (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    active_count bigint NOT NULL CHECK (active_count >= 0)
);

INSERT INTO app.active_admin_guard (singleton, active_count)
SELECT true, count(*) FROM app.users WHERE role = 'ADMIN' AND status = 'ACTIVE';

CREATE FUNCTION app.maintain_active_admin_guard() RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $function$
DECLARE
    was_active_admin boolean := false;
    is_active_admin boolean := false;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        was_active_admin := OLD.role = 'ADMIN' AND OLD.status = 'ACTIVE';
    END IF;
    IF TG_OP <> 'DELETE' THEN
        is_active_admin := NEW.role = 'ADMIN' AND NEW.status = 'ACTIVE';
    END IF;

    IF was_active_admin AND NOT is_active_admin THEN
        UPDATE app.active_admin_guard
           SET active_count = active_count - 1
         WHERE singleton AND active_count > 1;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'cannot remove the final active administrator'
                USING ERRCODE = '23514', CONSTRAINT = 'users_last_active_admin';
        END IF;
    ELSIF NOT was_active_admin AND is_active_admin THEN
        UPDATE app.active_admin_guard SET active_count = active_count + 1 WHERE singleton;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'active administrator guard is missing'
                USING ERRCODE = '23514', CONSTRAINT = 'users_active_admin_guard';
        END IF;
    END IF;

    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END
$function$;

CREATE TRIGGER users_active_admin_guard
BEFORE INSERT OR DELETE OR UPDATE OF role, status ON app.users
FOR EACH ROW EXECUTE FUNCTION app.maintain_active_admin_guard();

REVOKE ALL ON app.admin_audit_events, app.active_admin_guard FROM PUBLIC;
REVOKE ALL ON FUNCTION app.maintain_active_admin_guard() FROM PUBLIC;

---- create above / drop below ----

-- Forward-only by design: rollback would erase the audit trail and remove the
-- database-level invariant that protects the last active administrator.
DO $migration$
BEGIN
    RAISE EXCEPTION 'Admin account-management migration is forward-only';
END
$migration$;
