CREATE TABLE app.task_templates (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES app.users(id) ON DELETE CASCADE,
    name varchar(100) NOT NULL,
    description text NULL,
    version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CONSTRAINT task_templates_owner_id_unique UNIQUE (id, user_id),
    CONSTRAINT task_templates_name_valid CHECK (char_length(btrim(name)) BETWEEN 1 AND 100),
    CONSTRAINT task_templates_description_size CHECK (description IS NULL OR char_length(description) <= 10000),
    CONSTRAINT task_templates_version_positive CHECK (version > 0)
);

CREATE INDEX task_templates_owner_updated_idx
    ON app.task_templates (user_id, updated_at DESC, id DESC);

CREATE TABLE app.task_template_items (
    template_id uuid NOT NULL,
    user_id uuid NOT NULL,
    position integer NOT NULL,
    title varchar(200) NOT NULL,
    description text NULL,
    CONSTRAINT task_template_items_pk PRIMARY KEY (template_id, position),
    CONSTRAINT task_template_items_template_owner_fk
        FOREIGN KEY (template_id, user_id)
        REFERENCES app.task_templates (id, user_id)
        ON DELETE CASCADE,
    CONSTRAINT task_template_items_position_valid CHECK (position BETWEEN 0 AND 49),
    CONSTRAINT task_template_items_title_valid CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
    CONSTRAINT task_template_items_description_size CHECK (description IS NULL OR char_length(description) <= 10000)
);

CREATE UNIQUE INDEX task_template_items_title_unique
    ON app.task_template_items (template_id, lower(btrim(title)));

CREATE FUNCTION app.enforce_task_template_item_count() RETURNS trigger
LANGUAGE plpgsql AS $function$
DECLARE
    checked_template_id uuid;
    item_count integer;
BEGIN
    IF TG_TABLE_NAME = 'task_templates' THEN
        checked_template_id := NEW.id;
    ELSIF TG_OP = 'DELETE' THEN
        checked_template_id := OLD.template_id;
    ELSE
        checked_template_id := NEW.template_id;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM app.task_templates WHERE id = checked_template_id) THEN
        RETURN NULL;
    END IF;

    SELECT count(*)::integer INTO item_count
    FROM app.task_template_items WHERE template_id = checked_template_id;

    IF item_count < 1 OR item_count > 50 THEN
        RAISE EXCEPTION 'task template must have between 1 and 50 items'
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END
$function$;

CREATE CONSTRAINT TRIGGER task_templates_items_count_check
    AFTER INSERT OR UPDATE ON app.task_templates
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION app.enforce_task_template_item_count();

CREATE CONSTRAINT TRIGGER task_template_items_count_check
    AFTER INSERT OR UPDATE OR DELETE ON app.task_template_items
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION app.enforce_task_template_item_count();
