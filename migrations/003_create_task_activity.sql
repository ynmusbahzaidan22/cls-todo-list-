CREATE TABLE app.task_activity (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id uuid NOT NULL,
    user_id uuid NOT NULL,
    actor_user_id uuid NOT NULL,
    event_type text NOT NULL,
    previous_value jsonb NULL,
    new_value jsonb NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT task_activity_task_owner_fk
        FOREIGN KEY (task_id, user_id)
        REFERENCES app.tasks (id, user_id)
        ON DELETE CASCADE,
    CONSTRAINT task_activity_actor_fk
        FOREIGN KEY (actor_user_id)
        REFERENCES app.users (id)
        ON DELETE CASCADE,
    CONSTRAINT task_activity_actor_is_owner CHECK (actor_user_id = user_id),
    CONSTRAINT task_activity_event_type_valid CHECK (event_type IN (
        'TASK_CREATED',
        'TASK_TITLE_UPDATED',
        'TASK_DESCRIPTION_UPDATED',
        'TASK_UPDATED',
        'TASK_STATUS_CHANGED',
        'TASK_DELETED',
        'TASK_RESTORED'
    )),
    CONSTRAINT task_activity_values_object CHECK (
        (previous_value IS NULL OR jsonb_typeof(previous_value) = 'object')
        AND (new_value IS NULL OR jsonb_typeof(new_value) = 'object')
    )
);

CREATE INDEX task_activity_owner_task_created_idx
    ON app.task_activity (user_id, task_id, created_at DESC, id DESC);
