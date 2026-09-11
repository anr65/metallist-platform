---
name: metallist-ledger
description: Design, implement, or review Metallist accounting events, balances, debt, commissions, expenses, journal entries, and financial reports. Use whenever a change can affect money, cash custody, merchant settlement, P&L, reconciliation, or historical financial state.
---

# Metallist ledger safety

Use `metallist-domain` together with this skill when available. Read the relevant accepted ADRs and `docs/accounting/` files before changing financial behavior. Do not invent a chart of accounts or recognition rule when the documentation has not settled it.

## Separate the layers

Model these independently:

1. Source evidence: XLSX, Telegram message, receipt, manual report.
2. Business document or event: registry, funding, withdrawal, handover, repayment, expense, commission.
3. Workflow: draft, validated, confirmed, rejected, cancelled, or other documented states.
4. Accounting effect: journal entry and postings.

A draft or imported row must not affect official balances until the documented confirmation point.

## Ledger invariants

- Every posted journal entry balances exactly: total debit equals total credit.
- Store money as integer minor units or an exact fixed-precision decimal. Never use binary floating point.
- Store currency explicitly, even if the initial release supports only RUB.
- Posted entries are immutable. Correct an error with a linked reversal and, when needed, a replacement entry.
- Make posting idempotent with a stable business key so retries cannot duplicate money.
- Record event time, posting time, actor, source, reason, and links to the originating object.
- Derive account balances from postings or verified aggregates; do not maintain an unrelated editable balance as another source of truth.
- Keep balance observations from banks, cards, or operators separate and use them for reconciliation.
- Preserve per-merchant and per-custodian dimensions where they are required to explain debt and cash location.
- Reject mixed-currency entries unless an explicit exchange model exists.

## Economic classification

Merchant principal passing through cards and cash is transit movement, not revenue. Commission recognition, expense recognition, shortages, surpluses, bank fees, and outsource charges must follow documented posting rules. If a rule is missing, propose the rule and examples for approval before implementing it.

Do not derive profit from net cash movement. Reports must distinguish at least cash position, assets/liabilities, merchant debt, revenue, expenses, and profit.

## Required tests

Cover each affected event with tests for:

- balanced postings and correct dimensions;
- successful idempotent retry;
- duplicate rejection;
- reversal and replacement;
- partial amounts where permitted;
- invalid status transitions;
- rounding and boundary amounts;
- reconciliation without mutation of posted history;
- report totals agreeing with ledger totals.

Use a dedicated disposable test database for database-capable tests. Never point tests or migration experiments at production.

## Review output

When reviewing a financial change, report the affected event, recognition moment, accounts/dimensions, idempotency key, reversal path, reporting impact, migration impact, and missing decisions.
