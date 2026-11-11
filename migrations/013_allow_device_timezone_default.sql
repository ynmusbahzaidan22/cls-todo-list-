-- NULL means the account follows the browser/device IANA timezone. A concrete
-- value remains a persisted account-wide manual override. Existing values are
-- preserved so existing preferences do not change during upgrade.
ALTER TABLE app.users
    ALTER COLUMN time_zone DROP NOT NULL,
    ALTER COLUMN time_zone DROP DEFAULT;

---- create above / drop below ----

DO $migration$
BEGIN
    IF EXISTS (SELECT 1 FROM app.users WHERE time_zone IS NULL) THEN
        RAISE EXCEPTION 'cannot restore required time_zone while device-default preferences exist; set an explicit IANA timezone before rolling back';
    END IF;
END
$migration$;

ALTER TABLE app.users
    ALTER COLUMN time_zone SET DEFAULT 'UTC',
    ALTER COLUMN time_zone SET NOT NULL;
