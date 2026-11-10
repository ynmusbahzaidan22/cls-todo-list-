CREATE INDEX tasks_owner_deleted_at_idx
    ON app.tasks (user_id, deleted_at DESC, id DESC)
    WHERE deleted_at IS NOT NULL;

---- create above / drop below ----

DROP INDEX app.tasks_owner_deleted_at_idx;
