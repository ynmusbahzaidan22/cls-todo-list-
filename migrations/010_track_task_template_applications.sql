ALTER TABLE app.task_template_items
    ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid();

ALTER TABLE app.task_template_items
    DROP CONSTRAINT task_template_items_pk,
    ADD CONSTRAINT task_template_items_pk PRIMARY KEY (id),
    ADD CONSTRAINT task_template_items_identity_owner_unique UNIQUE (id, template_id, user_id);

CREATE INDEX task_template_items_order_idx
    ON app.task_template_items (template_id, user_id, position);

CREATE TABLE app.task_template_applications (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    template_id uuid NOT NULL,
    user_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT task_template_applications_identity_owner_unique UNIQUE (id, template_id, user_id),
    CONSTRAINT task_template_applications_template_owner_fk
        FOREIGN KEY (template_id, user_id)
        REFERENCES app.task_templates (id, user_id)
        ON DELETE CASCADE
);

ALTER TABLE app.task_templates
    ADD COLUMN latest_application_id uuid NULL,
    ADD CONSTRAINT task_templates_latest_application_fk
        FOREIGN KEY (latest_application_id, id, user_id)
        REFERENCES app.task_template_applications (id, template_id, user_id)
        DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE app.task_template_application_items (
    application_id uuid NOT NULL,
    template_id uuid NOT NULL,
    user_id uuid NOT NULL,
    template_item_id uuid NOT NULL,
    task_id uuid NOT NULL,
    CONSTRAINT task_template_application_items_pk PRIMARY KEY (application_id, template_item_id),
    CONSTRAINT task_template_application_items_application_fk
        FOREIGN KEY (application_id, template_id, user_id)
        REFERENCES app.task_template_applications (id, template_id, user_id)
        ON DELETE CASCADE,
    CONSTRAINT task_template_application_items_template_item_fk
        FOREIGN KEY (template_item_id, template_id, user_id)
        REFERENCES app.task_template_items (id, template_id, user_id)
        ON DELETE CASCADE,
    CONSTRAINT task_template_application_items_task_owner_fk
        FOREIGN KEY (task_id, user_id)
        REFERENCES app.tasks (id, user_id)
        ON DELETE CASCADE,
    CONSTRAINT task_template_application_items_task_unique UNIQUE (task_id)
);

CREATE INDEX task_template_application_items_task_idx
    ON app.task_template_application_items (user_id, task_id);

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.task_template_applications) THEN
        RAISE EXCEPTION 'cannot roll back template application tracking while application history exists';
    END IF;
END
$migration$;

ALTER TABLE app.task_templates
    DROP CONSTRAINT task_templates_latest_application_fk,
    DROP COLUMN latest_application_id;

DROP TABLE app.task_template_application_items;
DROP TABLE app.task_template_applications;

DROP INDEX app.task_template_items_order_idx;

ALTER TABLE app.task_template_items
    DROP CONSTRAINT task_template_items_identity_owner_unique,
    DROP CONSTRAINT task_template_items_pk,
    DROP COLUMN id,
    ADD CONSTRAINT task_template_items_pk PRIMARY KEY (template_id, position);
