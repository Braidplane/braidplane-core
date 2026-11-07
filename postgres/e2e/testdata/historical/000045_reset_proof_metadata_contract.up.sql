-- Archived transition regression fixture; outside the applied Core migration catalog.
-- The pre-v1 Proof contract intentionally discards old evidence and policies.
-- Connections, candidates, pipelines, deployments, provider data and audit history survive.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
DO $$
DECLARE
    tenant RECORD;
    affected BIGINT;
BEGIN
    LOCK TABLE proof_receipts, proof_evidence, proof_lifecycle_links,
        proof_observation_jobs, proof_observation_policies, execution_requests
        IN ACCESS EXCLUSIVE MODE;

    SELECT (SELECT COUNT(*) FROM proof_receipts)
         + (SELECT COUNT(*) FROM proof_evidence)
         + (SELECT COUNT(*) FROM proof_lifecycle_links)
         + (SELECT COUNT(*) FROM proof_observation_jobs)
         + (SELECT COUNT(*) FROM proof_observation_policies)
         + (SELECT COUNT(*) FROM execution_requests WHERE kind = 'proof_metadata')
    INTO affected;
    IF affected > 100000 THEN
        RAISE EXCEPTION 'Proof contract reset exceeds the reviewed 100000-record bound';
    END IF;

    FOR tenant IN
        SELECT organization.id, organization.slug,
            (SELECT COUNT(*) FROM proof_receipts WHERE organization_id = organization.id) AS receipts,
            (SELECT COUNT(*) FROM proof_evidence WHERE organization_id = organization.id) AS evidence,
            (SELECT COUNT(*) FROM proof_lifecycle_links WHERE organization_id = organization.id) AS links,
            (SELECT COUNT(*) FROM proof_observation_jobs WHERE organization_id = organization.id) AS jobs,
            (SELECT COUNT(*) FROM proof_observation_policies WHERE organization_id = organization.id) AS policies,
            (SELECT COUNT(*) FROM execution_requests WHERE organization_id = organization.id AND kind = 'proof_metadata') AS executions
        FROM organizations organization
    LOOP
        IF tenant.receipts + tenant.evidence + tenant.links + tenant.jobs + tenant.policies + tenant.executions = 0 THEN
            CONTINUE;
        END IF;
        INSERT INTO events (organization_id, slug, actor_type, entity_type, entity_slug,
            action, event_class, schema_version, metadata)
        VALUES (tenant.id, 'evt-proof-contract-reset-bp1165', 'system', 'organization', tenant.slug,
            'delete', 'security_audit', 1, jsonb_build_object(
                'reason', 'BP-1165 connector-native Proof contract reset',
                'receipts', tenant.receipts, 'evidence', tenant.evidence,
                'lifecycle_links', tenant.links, 'observation_jobs', tenant.jobs,
                'observation_policies', tenant.policies, 'proof_executions', tenant.executions));

        DELETE FROM execution_requests WHERE organization_id = tenant.id AND kind = 'proof_metadata';
        DELETE FROM proof_observation_jobs WHERE organization_id = tenant.id;
        DELETE FROM proof_observation_policies WHERE organization_id = tenant.id;
        DELETE FROM proof_lifecycle_links WHERE organization_id = tenant.id;
        UPDATE proof_receipts SET supersedes_receipt_id = NULL WHERE organization_id = tenant.id;
        DELETE FROM proof_receipts WHERE organization_id = tenant.id;
        DELETE FROM proof_evidence WHERE organization_id = tenant.id;
    END LOOP;
END $$;
