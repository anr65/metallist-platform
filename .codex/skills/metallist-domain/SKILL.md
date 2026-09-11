---
name: metallist-domain
description: Apply the agreed Metallist business vocabulary, workflows, roles, and documentation hierarchy when designing, implementing, reviewing, or explaining this product. Use for any change whose meaning depends on merchants, cards, registries, withdrawals, cash custody, expenses, commissions, debt, or operational status.
---

# Metallist domain guidance

Treat the repository documentation as the product contract, not as optional commentary.

## Resolve the source of truth

Before making a domain-sensitive change, locate and read the relevant files. Prefer them in this order:

1. Accepted records in `docs/decisions/`.
2. `docs/accounting/` for money, debt, commission, and posting semantics.
3. `docs/to-be/` for intended behavior and permissions.
4. `docs/as-is/` for observed current operations.
5. `docs/glossary.md` for canonical terms.
6. Source spreadsheets and messages as evidence, not as normative specifications.

Follow `docs/00-index.md` when it exists. If required documentation is absent or contradictory, state the uncertainty. Ask for a decision before implementing behavior that would change balances, debt, profit, permissions, or irreversible history.

## Preserve the domain boundaries

- A merchant (`мерчант`) requests or funds card payments connected to scrap suppliers.
- A card is a payment instrument and custody object, not the accounting transaction itself. The last four digits are not a globally unique identifier.
- A registry (`реестр`) is an imported business document. Its rows may propose operations but are not automatically ledger entries.
- Funding, withdrawal, cash handover, merchant repayment, expense, commission accrual, and correction are distinct events with independent evidence and timestamps.
- Cash custody must remain attributable to a location or responsible holder such as an operator, collector, or chief administrator.
- Merchant principal moving through cards and cash is transit turnover. Do not classify it as Metallist revenue. Commission is revenue only according to the agreed recognition rule.
- Cashflow, profit and loss, assets, and payables are different views and must not be collapsed into one mutable balance field.

Never infer a confirmed event from an observed balance alone. Store observations and reconciliations separately from facts such as a confirmed withdrawal or handover.

## Data and behavior rules

- Give business objects stable internal IDs. Keep external identifiers and source references separately.
- Record `occurred_at`, `recorded_at`, actor, source, status, and supporting document where applicable.
- Keep operational workflow state separate from accounting posting state.
- Make confirmation and reversal explicit actions with auditable actors and reasons.
- Do not store card PINs. Minimize full card numbers and personal data; prefer tokenized references and last four digits for display.
- Do not silently reinterpret legacy spreadsheet signs, colors, or formulas. Document their meaning before migration.

## Completion check

For a domain-sensitive change, verify that terminology, state transitions, permissions, reporting consequences, and documentation agree. Name unresolved business decisions instead of embedding guesses in code.
