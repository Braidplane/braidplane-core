CREATE INDEX pipeline_runs_execution_control_idx
    ON pipeline_runs (project_environment_id, pipeline_slug, started_at DESC, id DESC)
    WHERE status IN ('queued', 'running');
CREATE INDEX pipeline_runs_execution_drain_idx
    ON pipeline_runs (project_environment_id, pipeline_slug, finished_xid)
    WHERE finished_xid IS NOT NULL;

-- Repair preflight resets written by versions that only reset the delivery row.
UPDATE pipeline_runs pr
SET status = 'queued',
    timeline = jsonb_set(jsonb_set(pr.timeline, '{status}', '"queued"'), '{events}',
        jsonb_path_query_array(pr.timeline, '$.events[*] ? (@.type != "run_started")'))
FROM pipeline_webhook_deliveries pwd
WHERE pr.source_webhook_delivery_id = pwd.id AND pr.project_environment_id = pwd.project_environment_id
    AND pr.pipeline_slug = pwd.pipeline_slug AND pr.source_attempt_number = pwd.attempt_number
    AND pr.status = 'running' AND COALESCE((pr.timeline->>'revision')::bigint, 0) = 0
    AND NOT (pr.timeline ? 'structural_checks')
    AND pwd.status = 'accepted' AND pwd.decision_reason = 'execution_preflight_retry';
