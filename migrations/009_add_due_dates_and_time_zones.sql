ALTER TABLE app.tasks
    ADD COLUMN due_date date NULL,
    ADD CONSTRAINT tasks_due_date_finite CHECK (due_date IS NULL OR isfinite(due_date));

ALTER TABLE app.users
    ADD COLUMN time_zone text NOT NULL DEFAULT 'UTC',
    ADD CONSTRAINT users_time_zone_length CHECK (char_length(time_zone) BETWEEN 1 AND 100);

ALTER TABLE app.task_template_items
    ADD COLUMN due_in_days integer NULL,
    ADD CONSTRAINT task_template_items_due_in_days_valid
        CHECK (due_in_days IS NULL OR due_in_days BETWEEN 0 AND 3650);

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_event_type_valid,
    ADD CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED', 'TASK_TITLE_UPDATED', 'TASK_DESCRIPTION_UPDATED', 'TASK_UPDATED',
        'TASK_STATUS_CHANGED', 'TASK_DELETED', 'TASK_RESTORED', 'TASK_UNDONE',
        'TASK_PRIORITY_CHANGED', 'TASK_DUE_DATE_CHANGED'
    ));

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.tasks WHERE due_date IS NOT NULL)
       OR EXISTS (SELECT 1 FROM app.task_template_items WHERE due_in_days IS NOT NULL)
       OR EXISTS (SELECT 1 FROM app.users WHERE time_zone <> 'UTC')
       OR EXISTS (SELECT 1 FROM app.task_activity WHERE event_type = 'TASK_DUE_DATE_CHANGED') THEN
        RAISE EXCEPTION 'cannot roll back due date migration while due dates, template offsets, timezone preferences, or due date history exist';
    END IF;
END
$migration$;

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_event_type_valid,
    ADD CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED', 'TASK_TITLE_UPDATED', 'TASK_DESCRIPTION_UPDATED', 'TASK_UPDATED',
        'TASK_STATUS_CHANGED', 'TASK_DELETED', 'TASK_RESTORED', 'TASK_UNDONE', 'TASK_PRIORITY_CHANGED'
    ));

ALTER TABLE app.task_template_items
    DROP CONSTRAINT task_template_items_due_in_days_valid,
    DROP COLUMN due_in_days;
ALTER TABLE app.users
    DROP CONSTRAINT users_time_zone_length,
    DROP COLUMN time_zone;
ALTER TABLE app.tasks
    DROP CONSTRAINT tasks_due_date_finite,
    DROP COLUMN due_date;
