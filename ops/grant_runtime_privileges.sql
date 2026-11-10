\set ON_ERROR_STOP on

\if :{?runtime_role}
\else
\set runtime_role todo_runtime
\endif

SELECT format('GRANT CONNECT ON DATABASE %I TO %I', current_database(), :'runtime_role')
\gexec
SELECT format('GRANT USAGE ON SCHEMA app TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT ON app.users TO %I', :'runtime_role')
\gexec
SELECT format('GRANT UPDATE (time_zone) ON app.users TO %I', :'runtime_role')
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
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON app.task_template_applications, app.task_template_application_items TO %I', :'runtime_role')
\gexec
SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON app.auth_rate_limits TO %I', :'runtime_role')
\gexec
SELECT format('GRANT EXECUTE ON FUNCTION app.enforce_task_template_item_count() TO %I', :'runtime_role')
\gexec
