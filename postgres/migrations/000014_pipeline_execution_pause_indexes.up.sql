CREATE INDEX pipeline_runs_execution_control_idx
    ON pipeline_runs (project_environment_id, pipeline_slug, started_at DESC, id DESC)
    WHERE status IN ('queued', 'running');
CREATE INDEX pipeline_runs_execution_drain_idx
    ON pipeline_runs (project_environment_id, pipeline_slug, finished_xid)
    WHERE finished_xid IS NOT NULL;

-- Repair unadmitted webhook runs that were left running by the old reset.
-- Only deliveries with a retained runnable queue job return to the queue.
WITH preflight AS (
    SELECT pr.id, pwd.id AS delivery_id,
        COALESCE(job.kind = 'pipeline_webhook_delivery'
            AND job.state IN ('available', 'pending', 'retryable', 'running', 'scheduled')
            AND job.args->>'receiver_id' = pwd.receiver_id
            AND job.args->>'delivery_id' = pwd.delivery_id
            AND job.args->>'run_attempt' = pwd.attempt_number::text
            AND COALESCE(job.args->>'finalize_only', 'false') = 'false', false) AS retryable
    FROM pipeline_runs pr
    JOIN pipeline_webhook_deliveries pwd ON pr.source_webhook_delivery_id = pwd.id
        AND pr.project_environment_id = pwd.project_environment_id AND pr.pipeline_slug = pwd.pipeline_slug
        AND pr.source_attempt_number = pwd.attempt_number
    LEFT JOIN river_job job ON job.id = pr.worker_job_id
    WHERE pr.status = 'running' AND pr.source_type = 'webhook' AND COALESCE((pr.timeline->>'revision')::bigint, 0) = 0
        AND NOT (pr.timeline ? 'structural_checks')
        AND pwd.status = 'accepted' AND pwd.decision_reason = 'execution_preflight_retry'
), repaired AS (
    UPDATE pipeline_runs pr
    SET status = CASE WHEN p.retryable THEN 'queued' ELSE 'failed' END,
        ended_at = CASE WHEN NOT p.retryable THEN statement_timestamp() END,
        failure_kind = CASE WHEN NOT p.retryable THEN 'platform' END,
        error_summary = CASE WHEN NOT p.retryable THEN 'Webhook preflight retry has no runnable queue job' END,
        timeline = CASE WHEN p.retryable THEN
            jsonb_set(jsonb_set(pr.timeline, '{status}', '"queued"'), '{events}',
                jsonb_path_query_array(pr.timeline, '$.events[*] ? (@.type != "run_started")'))
        ELSE pr.timeline || jsonb_build_object(
            'status', 'failed',
            'failure', jsonb_build_object('at', statement_timestamp(), 'kind', 'platform',
                'error', 'Webhook preflight retry has no runnable queue job'),
            'events', COALESCE(NULLIF(pr.timeline->'events', 'null'::jsonb), '[]'::jsonb) ||
                jsonb_build_array(jsonb_build_object('at', statement_timestamp(), 'status', 'failed', 'type', 'run_completed')))
        END
    FROM preflight p WHERE pr.id = p.id
    RETURNING p.delivery_id, p.retryable
)
UPDATE pipeline_webhook_deliveries pwd
SET status = 'failed', failure_kind = 'platform', decision_reason = 'execution_rejected',
    completed_at = statement_timestamp(), updated_at = statement_timestamp()
FROM repaired r WHERE pwd.id = r.delivery_id AND NOT r.retryable;
