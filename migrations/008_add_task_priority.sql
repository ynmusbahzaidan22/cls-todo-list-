ALTER TABLE app.tasks
    ADD COLUMN priority text NOT NULL DEFAULT 'MEDIUM',
    ADD CONSTRAINT tasks_priority_valid CHECK (priority IN ('LOW', 'MEDIUM', 'HIGH', 'URGENT'));

ALTER TABLE app.task_template_items
    ADD COLUMN priority text NOT NULL DEFAULT 'MEDIUM',
    ADD CONSTRAINT task_template_items_priority_valid CHECK (priority IN ('LOW', 'MEDIUM', 'HIGH', 'URGENT'));

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_event_type_valid,
    ADD CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED',
        'TASK_TITLE_UPDATED',
        'TASK_DESCRIPTION_UPDATED',
        'TASK_UPDATED',
        'TASK_STATUS_CHANGED',
        'TASK_DELETED',
        'TASK_RESTORED',
        'TASK_UNDONE',
        'TASK_PRIORITY_CHANGED'
    ));

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.tasks WHERE priority <> 'MEDIUM')
       OR EXISTS (SELECT 1 FROM app.task_template_items WHERE priority <> 'MEDIUM')
       OR EXISTS (SELECT 1 FROM app.task_activity WHERE event_type = 'TASK_PRIORITY_CHANGED') THEN
        RAISE EXCEPTION 'cannot roll back priority migration while non-default priority data or history exists';
    END IF;
END
$migration$;

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_event_type_valid,
    ADD CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED',
        'TASK_TITLE_UPDATED',
        'TASK_DESCRIPTION_UPDATED',
        'TASK_UPDATED',
        'TASK_STATUS_CHANGED',
        'TASK_DELETED',
        'TASK_RESTORED',
        'TASK_UNDONE'
    ));

ALTER TABLE app.task_template_items
    DROP CONSTRAINT task_template_items_priority_valid,
    DROP COLUMN priority;

ALTER TABLE app.tasks
    DROP CONSTRAINT tasks_priority_valid,
    DROP COLUMN priority;
