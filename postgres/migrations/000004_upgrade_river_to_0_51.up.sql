-- SPDX-License-Identifier: MPL-2.0
-- River v0.51.2 main migrations 7 and 8, applied forward from the pinned bootstrap.
-- Source: github.com/riverqueue/river/riverdriver/riverpgxv5 v0.51.2.
CREATE TABLE river_notification (
    id bigserial PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    payload text NOT NULL,
    topic text NOT NULL,
    CONSTRAINT topic_length CHECK (length(topic) > 0 AND length(topic) < 128)
);
CREATE INDEX river_notification_created_at_idx ON river_notification (created_at);
CREATE INDEX river_notification_topic_id_idx ON river_notification (topic, id);

DROP TABLE river_client_queue;
DROP TABLE river_client;
ALTER TABLE river_job ALTER COLUMN max_attempts SET DEFAULT 25;
ALTER TABLE river_queue ALTER COLUMN updated_at SET DEFAULT CURRENT_TIMESTAMP;

-- River migration 8 is a no-op on PostgreSQL, which already uses sequences.
INSERT INTO river_migration(line, version) VALUES ('main', 7), ('main', 8);
