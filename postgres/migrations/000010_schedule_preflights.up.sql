ALTER TABLE pipeline_triggers
    ADD COLUMN schedule_preflight_lead_minutes integer,
    ADD CONSTRAINT chk_pipeline_triggers_preflight_lead CHECK (
        schedule_preflight_lead_minutes IS NULL OR (
            trigger_type = 'schedule' AND schedule_preflight_lead_minutes BETWEEN 1 AND 1440
        )
    );

CREATE TABLE pipeline_schedule_preflights (
    pipeline_trigger_id bigint NOT NULL REFERENCES pipeline_triggers (id) ON DELETE CASCADE,
    trigger_revision bigint NOT NULL,
    scheduled_fire_at timestamptz NOT NULL,
    preflight_at timestamptz NOT NULL,
    pipeline_slug text NOT NULL,
    pipeline_version_hash text,
    resolution_digest text,
    schema_refreshes jsonb,
    diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
    completed_at timestamptz,
    PRIMARY KEY (pipeline_trigger_id, trigger_revision, scheduled_fire_at)
);
