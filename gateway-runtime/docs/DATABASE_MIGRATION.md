# Gateway Database Migration and Runtime Startup

## Purpose

The Gateway runtime must not create or repair PostgreSQL schema/data as a side
effect of starting its CDC, API, hashing, aggregation, or Fabric workers.
Migration is a separate, explicit operator action.

This change does not move PostgreSQL or Hyperledger Fabric off the Besu server.
The mini PC runtime still connects to those services over the configured private
network. It only changes when schema migration is allowed to run.

## Commands

From `gateway-runtime`:

```sh
# Explicit, one-off migration. Requires DB_MIGRATION_DSN.
DB_MIGRATION_DSN='postgres://...' ./gateway-app migrate

# Normal service startup. Uses DB_DSN and performs schema-readiness checks only.
DB_DSN='postgres://...' ./gateway-app serve
```

For local source execution, the equivalent commands are `go run . migrate` and
`go run . serve`. The runtime command defaults to `serve` when no command is
given. Unknown commands and extra command arguments are rejected.

`migrate` deliberately does not fall back to `DB_DSN`: the operator must supply
the separate `DB_MIGRATION_DSN` so a normal service credential cannot
accidentally trigger DDL/DML. Keep both DSNs out of source control and logs.
Use a migration identity with only the permissions required for the reviewed
migration; use a separate least-privilege runtime identity where the deployment
supports it.

## Startup behavior

`serve` opens and pings PostgreSQL using `DB_DSN`, then performs read-only
catalog queries to verify required model tables, columns, and Gateway-specific
indexes. It exits before launching workers if a required element is missing and
prints a direction to run the explicit migration command.

The normal startup path does not call GORM `AutoMigrate`, `ALTER TABLE`, data
normalization `UPDATE`, or index `DROP`/`CREATE`. After schema validation, the
normal application continues to perform its intended business writes: ingesting
CDC records, updating audit/pipeline state, and anchoring to Fabric. This change
only separates migration work from boot; it does not make the runtime read-only.

## What the explicit migration changes

The existing migration is retained behind `migrate` and wrapped in a PostgreSQL
transaction. It:

- runs GORM `AutoMigrate` for the Gateway's registered models;
- sets defaults and nullable columns needed by snapshot, recovery, and user
  flows;
- fills missing/blank legacy snapshot event types, recovery CDC statuses, and
  tamper incident scopes;
- maps legacy tamper statuses (`UNDER_REVIEW`/`RECOVERING` to `OPEN`, and
  `DISMISSED` to `RESOLVED`);
- rebuilds the partial unique tamper-active index and creates required recovery
  and verification indexes.

There is no explicit `DELETE`, `TRUNCATE`, or `DROP TABLE` in this migration.
However, it is **not data-neutral**: the listed `UPDATE`s normalize existing
values/statuses, and `AutoMigrate` can create or alter schema elements. Do not
assume it is safe to run against production solely because it is transactional
or idempotent.

## Operational safety gate

Before running `migrate` against the Besu PostgreSQL database:

1. Review the current migration code and the exact Git commit being deployed.
2. Take a database backup and verify that it can be restored.
3. Restore that backup to an isolated staging PostgreSQL instance.
4. Run `migrate` on staging, inspect changed schema and affected row counts, and
   run Gateway tests/integration checks against that staging copy.
5. Obtain explicit operator approval for the production migration window.
6. Run the one-off command with `DB_MIGRATION_DSN`; verify its result, then
   start the normal service with `DB_DSN`.

This implementation task does not run the migration, connect to Besu, or
authorize a production schema/data change. A successful local build or test is
not proof that the migration is ready for production.
