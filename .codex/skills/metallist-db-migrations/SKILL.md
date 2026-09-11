---
name: metallist-db-migrations
description: Create, review, or execute database schema and data migrations for Metallist. Use when changing PostgreSQL tables, constraints, indexes, financial data representation, backfills, retention, or deployment compatibility.
---

# Metallist database migrations

Read `docs/architecture/database.md`, `docs/accounting/`, and relevant accepted ADRs before designing a migration. Use the repository's migration framework and conventions rather than adding a second mechanism.

## Environment gate

Before any database-capable validation, prove from effective runtime configuration that the target is a dedicated disposable test database. A variable name containing `test` is not proof. Do not run migrations, seeders, resets, or backfills against production unless the user explicitly requests that exact operation and the repository's production procedure is satisfied.

## Migration rules

- Prefer additive, backward-compatible expand/migrate/contract changes when more than one application version may run.
- Never delete or rewrite posted journal history to simplify a schema change.
- Preserve exact monetary values, currencies, timestamps, source references, actors, and audit links.
- Add database constraints for invariants that PostgreSQL can enforce reliably; keep cross-row accounting validation in a transactional posting boundary when appropriate.
- Use stable foreign keys and deliberate delete behavior. Financial records should normally be restricted or soft-retired, not cascaded away.
- Make idempotency and external-source uniqueness enforceable where possible.
- Plan indexes from actual access paths and inspect the write and locking cost of large changes.
- Make backfills resumable, observable, and deterministic. Record how completion is verified.
- Do not combine destructive cleanup with an unrelated feature migration.

## Plan destructive or locking changes

For a drop, narrowing conversion, large rewrite, new non-null column, uniqueness constraint, or bulk backfill, document affected rows, lock behavior, deployment order, rollback or roll-forward path, backup prerequisite, and verification query. Stop if the target or data-loss scope is unclear.

## Verification

On an isolated database, verify migration from the supported previous schema, application compatibility during rollout, constraints, indexes, representative backfill data, rollback when supported, and ledger/report reconciliation before and after. Update schema documentation and any affected ADR or operational runbook in the same change.
