---
name: metallist-release-gate
description: Assess Metallist release readiness and produce an evidence-backed go/no-go result. Use before staging or production releases, deployment handoff, or when the user asks whether a change is safe to ship; do not use as authorization to deploy.
---

# Metallist release gate

This is a verification workflow, not deployment authorization. Never deploy, tag, push, migrate production, or change external systems unless the user separately requested that action.

Read the release runbook, affected domain documentation, accepted ADRs, and the repository's own test commands. Scale checks to the changed surface, but do not waive a relevant financial or security gate because a diff is small.

## Establish safe test targets

Before running database-capable tests or migrations, prove that every effective target is disposable and isolated from production. If this cannot be proven, skip those commands, mark the gate blocked, and report the missing evidence.

## Gates

Evaluate the applicable gates in this order:

1. Change scope and documentation agree.
2. Formatting, static analysis, compilation, and unit tests pass.
3. Integration tests pass on isolated dependencies.
4. Ledger invariants, idempotency, reversals, and report reconciliation pass for financial changes.
5. Migrations pass from the supported previous schema and have a safe rollout plan.
6. Registry fixtures and duplicate/error cases pass for import changes.
7. Authorization, audit, secret handling, file-upload, and input-validation risks are covered.
8. Critical user journeys pass in browser tests when UI behavior changed.
9. Observability identifies failed imports, failed postings, reconciliation differences, and unexpected exceptions.
10. Release notes, operator steps, rollback or roll-forward steps, and backup prerequisites are current.

## Automatic no-go conditions

Return no-go for any relevant unresolved condition including:

- an unbalanced or mutable posted ledger entry;
- possible duplicate financial posting on retry;
- an unexplained change in merchant debt, cash custody, revenue, expense, or profit;
- destructive or untested migration;
- tests connected to a non-disposable database;
- authorization bypass or leaked secret/PIN/full sensitive card data;
- silent skipped import rows or unreconciled source totals;
- missing recovery path for an irreversible production change;
- failing required checks.

## Result format

Lead with `GO`, `NO-GO`, or `CONDITIONAL GO`. List evidence for each applicable gate, exact blockers, consciously accepted residual risks, and the next action required. Never report a command as passing unless it was actually run successfully in the current evaluated state.
