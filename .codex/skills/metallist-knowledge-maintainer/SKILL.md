---
name: metallist-knowledge-maintainer
description: Create and maintain Metallist's Markdown knowledge base as a consistent source of truth. Use when documenting AS IS or TO BE behavior, architecture, accounting rules, integrations, decisions, operational runbooks, or when code changes require documentation updates.
---

# Metallist knowledge maintenance

Keep knowledge in the repository so it is reviewed and versioned with the product. Prefer a small navigable set of canonical documents over duplicated explanations.

## Canonical structure

Use the established structure when present. For a new knowledge base, prefer:

```text
docs/
  00-index.md
  glossary.md
  as-is/
  to-be/
  accounting/
  architecture/
  integrations/
  api/
  operations/
  decisions/
  testing/
  security/
```

Do not create empty placeholder files merely to complete the tree. Add a document when it has maintained content and link it from `docs/00-index.md`.

## Separate kinds of truth

- `as-is/` records observed current practice and evidence, including exceptions and manual work.
- `to-be/` records approved intended behavior and acceptance conditions.
- `accounting/` defines recognition points, accounts, posting rules, reconciliation, and reporting semantics.
- `architecture/` explains components, data model, trust boundaries, and deployment shape.
- `decisions/` contains ADRs for consequential choices and their alternatives.
- `operations/` contains executable runbooks, not architectural aspirations.

Never silently turn an AS IS observation into a TO BE requirement. Mark proposals as proposed until approved.

## Documentation rules

- Use canonical terms from `docs/glossary.md`; add aliases used in spreadsheets or Telegram.
- State document status, last meaningful update, and unresolved decisions when useful.
- Link to the canonical rule rather than copying it into several files.
- Give financial rules concrete examples with dates, amounts, state transitions, and expected accounting effects.
- Tie API, schema, permissions, reports, and tests back to the same domain rule.
- When a decision changes, preserve history: supersede the ADR or rule and link the replacement.
- Keep examples anonymized. Never record tokens, passwords, PINs, private keys, full sensitive card data, or unnecessary personal data.
- Treat spreadsheets, chat exports, and interviews as attributed evidence with known limitations.

## Change impact routing

- Business workflow change: update AS IS or TO BE, lifecycle, permissions, and acceptance scenarios.
- Financial change: update recognition/posting rules, examples, reports, reconciliation, and ledger invariants.
- Database change: update the data model, constraints, migration notes, and affected integrations.
- Import change: update source format, mapping, validation, duplicate policy, and fixtures.
- Architecture change: create or supersede an ADR and update system context.
- Operational change: update deployment, monitoring, backup, recovery, or incident runbooks.

## Consistency review

Before finishing, search for the changed term or rule across documentation and code. Resolve or explicitly record contradictions, validate local links, ensure the index exposes new documents, and list decisions still awaiting approval. Do not describe unimplemented TO BE behavior as current production behavior.
