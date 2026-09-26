-- Grafana reads skyledger's tables only. 0001 used to grant pg_read_all_data,
-- which reads every database on the server (wrong on a shared server) and
-- needs superuser to grant (a Kubernetes tenant's owner isn't one).
-- Hypertables by name: TimescaleDB passes object grants on to their chunks. The schema form covers the rest.
GRANT SELECT ON positions, receiver_stats, history_positions TO skyledger_grafana;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO skyledger_grafana;
-- Binds to the role running migrate; a separate runtime user would need FOR ROLE.
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO skyledger_grafana;

-- Installs that ran the old 0001 (always as superuser) get the wide grant removed.
-- A non-superuser owner can't revoke it, so it warns instead of failing the migration.
DO $$
DECLARE
    wide boolean := EXISTS (SELECT FROM pg_auth_members m
                            JOIN pg_roles r ON r.oid = m.roleid AND r.rolname = 'pg_read_all_data'
                            JOIN pg_roles g ON g.oid = m.member AND g.rolname = 'skyledger_grafana');
BEGIN
    IF wide AND pg_has_role('pg_read_all_data', 'MEMBER WITH ADMIN OPTION') THEN
        REVOKE pg_read_all_data FROM skyledger_grafana;
    ELSIF wide THEN
        RAISE WARNING 'skyledger_grafana still has pg_read_all_data; revoke it as a superuser';
    END IF;
END $$;
