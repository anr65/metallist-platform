# Instructions for agents

This repository contains a financial operations system. Correctness, auditability, and preservation of history take priority over convenience.

## Required reading

Before domain-sensitive work, read:

1. `docs/00-index.md`;
2. relevant accepted ADRs in `docs/decisions/`;
3. relevant files in `docs/accounting/` and `docs/to-be/`;
4. `docs/open-questions.md`.

Use the matching project skills in `.codex/skills/`:

- `metallist-domain` for business behavior;
- `metallist-ledger` for money or reporting;
- `metallist-registry-import` for XLSX, Telegram, or legacy imports;
- `metallist-db-migrations` for PostgreSQL changes;
- `metallist-release-gate` before release readiness claims;
- `metallist-knowledge-maintainer` for documentation changes.

## Sources of truth

Apply information in this order:

1. accepted ADR;
2. approved accounting rule;
3. approved TO BE behavior;
4. documented AS IS observation;
5. spreadsheet, chat, or interview evidence;
6. explicit assumption.

Do not implement an assumption that changes debt, profit, balances, permissions, or irreversible history. Record the decision in `docs/open-questions.md` and request clarification.

## Financial safety

The Metallist production SSH host is `deploy@167.233.162.67`. Verify the service and deployment paths before any production action; do not use unrelated SSH hosts for this project.

- Separate source evidence, business documents, workflow state, and ledger postings.
- Posted journal entries are immutable. Correct them through reversal and replacement.
- Every journal entry must balance exactly.
- Store money as integer minor units or exact fixed-precision decimal, never floating point.
- Make every financial command idempotent.
- Transit merchant principal is not Metallist revenue.
- Never use production databases for tests or migration experiments.

## Sensitive data

Never commit tokens, passwords, PINs, private keys, full card data, production database dumps, or unredacted source registries. Use anonymized fixtures. Keep secrets in the deployment secret store.

## Documentation

Update documentation in the same change whenever behavior, accounting, schema, permissions, integrations, deployment, or operational procedures change. Preserve AS IS and TO BE as separate statements.
