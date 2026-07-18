\set ON_ERROR_STOP on

-- Required psql variables:
--   owner_role, audit_writer_role, migration_role, runtime_role, worker_role
-- The script creates NOLOGIN privilege groups. Deployment tooling creates login accounts and grants group membership.

SELECT pg_catalog.format('CREATE ROLE %I NOLOGIN', :'owner_role')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'owner_role'
) \gexec

SELECT pg_catalog.format('CREATE ROLE %I NOLOGIN', :'audit_writer_role')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'audit_writer_role'
) \gexec

SELECT pg_catalog.format('CREATE ROLE %I NOLOGIN', :'migration_role')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'migration_role'
) \gexec

SELECT pg_catalog.format('CREATE ROLE %I NOLOGIN', :'runtime_role')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'runtime_role'
) \gexec

SELECT pg_catalog.format('CREATE ROLE %I NOLOGIN', :'worker_role')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = :'worker_role'
) \gexec

SELECT pg_catalog.format('GRANT %I TO %I', :'owner_role', :'migration_role') \gexec
SELECT pg_catalog.format('GRANT %I TO %I', :'audit_writer_role', :'runtime_role') \gexec
SELECT pg_catalog.format('GRANT %I TO %I', :'audit_writer_role', :'worker_role') \gexec
