-- Run as the schema owner after runtime-grants.sql; login roles already exist.
BEGIN;
CREATE OR REPLACE FUNCTION pg_temp.check_executor_role(executor_name text, runtime_name text, migrator_name text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE executor_oid oid;
BEGIN
    SELECT oid INTO executor_oid FROM pg_roles
    WHERE rolname = executor_name AND NOT rolsuper AND NOT rolcreatedb
      AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls;
    IF executor_oid IS NULL OR executor_name IN (runtime_name, migrator_name) THEN
        RAISE EXCEPTION 'executor must be an unprivileged role distinct from runtime and migrator';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = executor_oid)
       OR EXISTS (SELECT 1 FROM pg_shdepend WHERE refclassid = 'pg_authid'::regclass
                  AND refobjid = executor_oid AND deptype = 'o') THEN
        RAISE EXCEPTION 'executor must have no memberships or owned objects';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = runtime_name)
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = migrator_name) THEN
        RAISE EXCEPTION 'runtime and migrator roles must already exist';
    END IF;
END;
$$;
SELECT pg_temp.check_executor_role(:'executor_role', :'runtime_role', :'migrator_role');
-- Explicit grants prevent default PUBLIC function privileges from expanding the executor.
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
-- PostgreSQL global defaults cannot be revoked by a per-schema default.
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role"
    REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO :"runtime_role";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role" IN SCHEMA public
    GRANT EXECUTE ON FUNCTIONS TO :"runtime_role";
REVOKE ALL ON SCHEMA public FROM :"executor_role";
GRANT USAGE ON SCHEMA public TO :"executor_role";
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM :"executor_role";
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM :"executor_role";
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM :"executor_role";
-- Startup checks read only the migration identity, never tenant data.
GRANT SELECT ON public.core_schema_migrations TO :"executor_role";
GRANT EXECUTE ON FUNCTION
    public.claim_next_execution_request(),
    public.heartbeat_execution_request(uuid, uuid),
    public.complete_execution_request(uuid, uuid, bytea, boolean),
    public.execution_request_metrics(),
    public.connector_execution_admission_metrics(),
    public.check_connector_dispatch(uuid, uuid, text, text, text, jsonb, text, timestamptz, boolean),
    public.snowflake_wif_ready(text, text, text, text)
TO :"executor_role";
COMMIT;
