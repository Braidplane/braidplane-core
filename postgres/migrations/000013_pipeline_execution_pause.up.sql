CREATE TABLE pipeline_execution_controls (
    project_id bigint NOT NULL,
    project_environment_id bigint NOT NULL,
    pipeline_slug varchar(100) NOT NULL,
    paused boolean NOT NULL DEFAULT false,
    revision bigint NOT NULL DEFAULT 0,
    changed_at timestamptz,
    pause_snapshot pg_snapshot,
    changed_by_type text,
    changed_by_slug varchar(100),
    PRIMARY KEY (project_environment_id, pipeline_slug),
    FOREIGN KEY (project_id, project_environment_id) REFERENCES project_environments(project_id, id) ON DELETE CASCADE,
    FOREIGN KEY (project_id, pipeline_slug) REFERENCES pipeline_specs(project_id, slug) ON DELETE CASCADE
);

ALTER TABLE pipeline_runs ADD COLUMN finished_xid xid8;

CREATE FUNCTION lock_pipeline_execution_control(target_project bigint, target_environment bigint, target_pipeline text)
RETURNS pipeline_execution_controls
LANGUAGE plpgsql AS $$
DECLARE
    control pipeline_execution_controls;
BEGIN
    -- Claims and control updates serialize on the same durable scope.
    INSERT INTO pipeline_execution_controls (project_id, project_environment_id, pipeline_slug)
    VALUES (target_project, target_environment, target_pipeline)
    ON CONFLICT DO NOTHING;

    SELECT * INTO STRICT control
    FROM pipeline_execution_controls
    WHERE project_environment_id = target_environment AND pipeline_slug = target_pipeline
    FOR SHARE;
    RETURN control;
EXCEPTION WHEN serialization_failure THEN
    RAISE EXCEPTION USING ERRCODE = 'BP279', MESSAGE = 'pipeline execution control changed during claim';
END;
$$;

CREATE FUNCTION fence_pipeline_run_start() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        IF NEW.status <> 'running' OR OLD.status = 'running' THEN
            RETURN NULL;
        END IF;
    ELSIF NEW.status <> 'running' AND (
        NEW.status <> 'queued' OR NEW.source_type IN ('schedule', 'webhook', 'completion')
    ) THEN
        RETURN NULL;
    END IF;

    IF (lock_pipeline_execution_control(NEW.project_id, NEW.project_environment_id, NEW.pipeline_slug)).paused THEN
        RAISE EXCEPTION USING ERRCODE = 'BP278', MESSAGE = 'pipeline execution is paused';
    END IF;
    RETURN NULL;
END;
$$;

CREATE TRIGGER pipeline_runs_fence_start
    AFTER INSERT OR UPDATE OF status ON pipeline_runs
    FOR EACH ROW EXECUTE FUNCTION fence_pipeline_run_start();


CREATE FUNCTION record_pipeline_run_finish() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- A pause compares commit visibility, so worker clocks and late commits cannot hide evidence.
    NEW.finished_xid := pg_current_xact_id();
    RETURN NEW;
END;
$$;

CREATE TRIGGER pipeline_runs_record_finish
    BEFORE UPDATE OF status ON pipeline_runs
    FOR EACH ROW
    WHEN (OLD.status IN ('queued', 'running') AND NEW.status IN ('succeeded', 'failed', 'canceled'))
    EXECUTE FUNCTION record_pipeline_run_finish();
