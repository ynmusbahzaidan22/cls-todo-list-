ALTER TABLE app.task_activity
    ADD CONSTRAINT task_activity_id_task_owner_unique UNIQUE (id, task_id, user_id);

ALTER TABLE app.task_activity
    ADD COLUMN undo_of_event_id uuid NULL,
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
    )),
    ADD CONSTRAINT task_activity_undo_reference_consistent CHECK (
        (event_type = 'TASK_UNDONE') = (undo_of_event_id IS NOT NULL)
    ),
    ADD CONSTRAINT task_activity_undo_original_fk
        FOREIGN KEY (undo_of_event_id, task_id, user_id)
        REFERENCES app.task_activity (id, task_id, user_id)
        ON DELETE CASCADE;

CREATE UNIQUE INDEX task_activity_undo_once_idx
    ON app.task_activity (undo_of_event_id)
    WHERE undo_of_event_id IS NOT NULL;

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.task_activity WHERE event_type = 'TASK_UNDONE') THEN
        RAISE EXCEPTION 'cannot roll back Undo migration while Undo history exists';
    END IF;
END
$migration$;

DROP INDEX app.task_activity_undo_once_idx;

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_undo_original_fk,
    DROP CONSTRAINT task_activity_undo_reference_consistent,
    DROP CONSTRAINT task_activity_event_type_valid,
    ADD CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED',
        'TASK_TITLE_UPDATED',
        'TASK_DESCRIPTION_UPDATED',
        'TASK_UPDATED',
        'TASK_STATUS_CHANGED',
        'TASK_DELETED',
        'TASK_RESTORED'
    )),
    DROP COLUMN undo_of_event_id;

ALTER TABLE app.task_activity
    DROP CONSTRAINT task_activity_id_task_owner_unique;
