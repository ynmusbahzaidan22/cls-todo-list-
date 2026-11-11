-- Tern runs each migration in a transaction. Refuse to change the schema if
-- existing template items already have ambiguous ordering; resolve those rows
-- explicitly before retrying this migration.
DO $migration$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM app.task_template_items
        GROUP BY template_id, position
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'cannot enforce unique template item positions: duplicate (template_id, position) rows exist; inspect app.task_template_items and resolve duplicates before retrying';
    END IF;
END
$migration$;

ALTER TABLE app.task_template_items
    ADD CONSTRAINT task_template_items_template_position_unique
    UNIQUE (template_id, position);

---- create above / drop below ----

-- Rollback removes only this constraint and preserves every template item.
ALTER TABLE app.task_template_items
    DROP CONSTRAINT task_template_items_template_position_unique;
