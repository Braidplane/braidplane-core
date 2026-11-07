-- Run after core migrations as the dedicated schema/migration owner with psql:
-- psql -v ON_ERROR_STOP=1 -v runtime_role=customer_runtime -v migrator_role=customer_migrator -f scripts/self-hosted-runtime-grants.sql
-- Roles must already exist. This script creates no login credentials.
BEGIN;
CREATE OR REPLACE FUNCTION pg_temp.check_runtime_role(runtime_name text, migrator_name text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE runtime_oid oid;
BEGIN
    SELECT oid INTO runtime_oid FROM pg_roles
    WHERE rolname = runtime_name AND NOT rolsuper AND NOT rolcreatedb
      AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls;
    IF runtime_oid IS NULL OR runtime_name = migrator_name THEN
        RAISE EXCEPTION 'runtime role must exist, be unprivileged and distinct from migrator';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = migrator_name) THEN
        RAISE EXCEPTION 'migrator role must exist';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = runtime_oid)
       OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname='public' AND c.relowner=runtime_oid) THEN
        RAISE EXCEPTION 'runtime role must have no inherited memberships or owned application objects';
    END IF;
    IF pg_has_role(runtime_oid, migrator_name, 'MEMBER')
       OR pg_has_role(runtime_oid, (SELECT datdba FROM pg_database WHERE datname = current_database()), 'MEMBER')
       OR pg_has_role(runtime_oid, (SELECT nspowner FROM pg_namespace WHERE nspname = 'public'), 'MEMBER') THEN
        RAISE EXCEPTION 'runtime role must not inherit migrator, database-owner or schema-owner authority';
    END IF;
END;
$$;
SELECT pg_temp.check_runtime_role(:'runtime_role', :'migrator_role');
REVOKE ALL ON SCHEMA public FROM PUBLIC;
REVOKE ALL ON SCHEMA public FROM :"runtime_role";
GRANT USAGE ON SCHEMA public TO :"runtime_role";
GRANT USAGE, CREATE ON SCHEMA public TO :"migrator_role";
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO :"runtime_role";
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO :"runtime_role";
REVOKE ALL ON TABLE public.core_schema_migrations FROM PUBLIC;
REVOKE ALL ON TABLE public.core_schema_migrations FROM :"runtime_role";
GRANT SELECT ON TABLE public.core_schema_migrations TO :"runtime_role";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role" IN SCHEMA public
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO :"runtime_role";
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_role" IN SCHEMA public
    GRANT USAGE, SELECT ON SEQUENCES TO :"runtime_role";
COMMIT;
