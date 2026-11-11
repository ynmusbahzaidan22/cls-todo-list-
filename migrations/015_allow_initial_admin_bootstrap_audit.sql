-- Permit a narrowly identified system event for the one-time initial Admin
-- bootstrap. Ordinary administrator audit events still require an actor.
ALTER TABLE app.admin_audit_events
    ALTER COLUMN actor_user_id DROP NOT NULL;

ALTER TABLE app.admin_audit_events
    DROP CONSTRAINT admin_audit_events_action_check;

ALTER TABLE app.admin_audit_events
    ADD CONSTRAINT admin_audit_events_action_actor_consistent CHECK (
        (action = 'INITIAL_ADMIN_BOOTSTRAPPED' AND actor_user_id IS NULL)
        OR
        (action IN ('USER_ROLE_CHANGED', 'USER_STATUS_CHANGED', 'USER_SESSIONS_REVOKED') AND actor_user_id IS NOT NULL)
    );

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.admin_audit_events WHERE action = 'INITIAL_ADMIN_BOOTSTRAPPED') THEN
        RAISE EXCEPTION 'cannot roll back initial Admin audit support after a bootstrap event has been recorded';
    END IF;
END
$migration$;

ALTER TABLE app.admin_audit_events
    DROP CONSTRAINT admin_audit_events_action_actor_consistent;

ALTER TABLE app.admin_audit_events
    ADD CONSTRAINT admin_audit_events_action_check CHECK (
        action IN ('USER_ROLE_CHANGED', 'USER_STATUS_CHANGED', 'USER_SESSIONS_REVOKED')
    );

ALTER TABLE app.admin_audit_events
    ALTER COLUMN actor_user_id SET NOT NULL;
