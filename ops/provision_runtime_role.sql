\set ON_ERROR_STOP on
\if :{?runtime_role}
\else
\set runtime_role todo_runtime
\endif
SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS', :'runtime_role')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'runtime_role')
\gexec

SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS', :'runtime_role')
\gexec

SELECT format('REVOKE %I FROM %I CASCADE', parent.rolname, :'runtime_role')
FROM pg_auth_members m
JOIN pg_roles parent ON parent.oid=m.roleid
JOIN pg_roles member ON member.oid=m.member
WHERE member.rolname=:'runtime_role'
\gexec

SELECT CASE WHEN rolpassword IS NULL THEN 'true' ELSE 'false' END AS runtime_password_missing
FROM pg_authid WHERE rolname = :'runtime_role' \gset
\if :runtime_password_missing
\echo Set an initial password for runtime role :runtime_role
\prompt -s runtime_password "Enter password (input hidden): "
SELECT format('ALTER ROLE %I PASSWORD %L', :'runtime_role', :'runtime_password')
\gexec
\unset runtime_password
\endif

SELECT format('REVOKE CREATE, TEMPORARY ON DATABASE %I FROM PUBLIC CASCADE', current_database())
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON DATABASE %I FROM %I CASCADE', current_database(), :'runtime_role')
\gexec
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'runtime_role')
\gexec
SELECT format('REVOKE CREATE ON SCHEMA public FROM PUBLIC')
\gexec
SELECT format('REVOKE CREATE ON SCHEMA public FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON SCHEMA app FROM PUBLIC')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON SCHEMA app FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('GRANT USAGE ON SCHEMA app TO %I', :'runtime_role')
\gexec
-- Keep PUBLIC and the runtime role from directly invoking app functions. The
-- runtime role does not create triggers; trigger actions remain owned by the
-- migration/object owner and do not grant this role function EXECUTE.
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC;
SELECT format('REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA app FROM %I CASCADE', :'runtime_role')
\gexec
REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA app FROM PUBLIC CASCADE;
SELECT format('REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA app FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('REVOKE SELECT (%1$I), INSERT (%1$I), UPDATE (%1$I), REFERENCES (%1$I) ON app.%2$I FROM PUBLIC CASCADE', att.attname, c.relname)
FROM pg_attribute att JOIN pg_class c ON c.oid=att.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='app' AND c.relkind IN ('r','p','v','m','f') AND att.attnum>0 AND NOT att.attisdropped
\gexec
SELECT format('REVOKE SELECT (%1$I), INSERT (%1$I), UPDATE (%1$I), REFERENCES (%1$I) ON app.%2$I FROM %3$I CASCADE', att.attname, c.relname, :'runtime_role')
FROM pg_attribute att JOIN pg_class c ON c.oid=att.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='app' AND c.relkind IN ('r','p','v','m','f') AND att.attnum>0 AND NOT att.attisdropped
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA app FROM PUBLIC CASCADE')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA app FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER, MAINTAIN ON app.users FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT ON app.users TO %I', :'runtime_role')
\gexec
SELECT format('GRANT UPDATE (time_zone, role, status, updated_at) ON app.users TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON app.sessions TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE ON app.tasks TO %I', :'runtime_role')
\gexec
SELECT format('REVOKE UPDATE, DELETE, TRUNCATE ON app.task_activity FROM %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT ON app.task_activity TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON app.task_templates, app.task_template_items TO %I', :'runtime_role')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON app.task_template_applications, app.task_template_application_items FROM %I CASCADE', :'runtime_role')
\gexec
SELECT format('REVOKE UPDATE (%I) ON app.%I FROM %I CASCADE', att.attname, c.relname, :'runtime_role')
FROM pg_attribute att
JOIN pg_class c ON c.oid = att.attrelid
WHERE c.oid IN ('app.task_template_applications'::regclass, 'app.task_template_application_items'::regclass)
  AND att.attnum > 0 AND NOT att.attisdropped
\gexec
SELECT format('GRANT SELECT, INSERT ON app.task_template_applications, app.task_template_application_items TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON app.auth_rate_limits TO %I', :'runtime_role')
\gexec
SELECT format('GRANT INSERT ON app.admin_audit_events TO %I', :'runtime_role')
\gexec
SELECT format('REVOKE ALL PRIVILEGES ON app.active_admin_guard FROM %I CASCADE', :'runtime_role')
\gexec
REVOKE CREATE ON SCHEMA app FROM PUBLIC;
