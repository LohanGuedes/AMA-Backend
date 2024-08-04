-- Write your migrate up statements here

CREATE TABLE IF NOT EXISTS messages (
    "id"                uuid            PRIMARY KEY NOT NULL DEFAULT gen_random_uuid(),
    "room_id"           uuid                        NOT NULL,
    "message"           VARCHAR(255)                NOT NULL,
    "reaction_count"    BIGINT                      NOT NULL default 0,
    "answered"          BOOLEAN                     NOT NULL default false,

    FOREIGN KEY(room_id) REFERENCES rooms(id)
);

---- create above / drop below ----

DROP DATABASE IF EXISTS messages;

-- Write your migrate down statements here. If this migration is irreversible
-- Then delete the separator line above.
