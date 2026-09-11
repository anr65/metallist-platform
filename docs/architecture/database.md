# Предварительная модель PostgreSQL

Статус: проект  
Дата: 2026-09-11

Точные типы, ограничения и имена уточняются перед первой миграцией. Деньги предполагаются как `bigint` в копейках, идентификаторы — UUID или другой единый непрогнозируемый тип.

## Identity и аудит

- `users(id, display_name, status, created_at)`
- `roles(id, code)`
- `user_roles(user_id, role_id)`
- `telegram_identities(user_id, telegram_user_id, status)`
- `audit_events(id, actor_user_id, action, object_type, object_id, occurred_at, metadata)`

## Справочники

- `merchants(id, code, name, status, created_at)`
- `banks(id, code, name)`
- `cards(id, bank_id, last4, token_ref, status, created_at, retired_at)`
- `card_limits(id, card_id, limit_type, period_type, amount_minor, currency, merchant_id, valid_from, valid_to)`
- `custodians(id, type, user_id, name, status)`
- `expense_categories(id, code, name, status)`

Полный номер и PIN не являются полями основной таблицы `cards`.

## Документы и импорт

- `source_documents(id, source_type, external_id, checksum, storage_key, received_at, received_by, metadata)`
- `import_batches(id, source_document_id, parser_version, status, created_at, approved_by, applied_at)`
- `import_rows(id, batch_id, sheet_name, row_number, raw_data, normalized_data, status, error_codes)`
- `merchant_registries(id, merchant_id, source_document_id, external_ref, occurred_at, status, total_minor, currency)`
- `merchant_registry_items(id, registry_id, row_id, card_id, amount_minor, currency, external_ref, status)`

## Операционные события

- `merchant_fundings(id, merchant_id, card_id, registry_item_id, amount_minor, currency, occurred_at, status, idempotency_key)`
- `withdrawals(id, card_id, custodian_id, amount_minor, currency, occurred_at, status, source_document_id, idempotency_key)`
- `card_balance_observations(id, card_id, observed_minor, currency, observed_at, reporter_id, source_document_id)`
- `cash_handovers(id, from_custodian_id, to_custodian_id, amount_minor, currency, occurred_at, status, idempotency_key)`
- `merchant_repayments(id, merchant_id, from_custodian_id, amount_minor, currency, occurred_at, status, idempotency_key)`
- `expenses(id, category_id, source_account_ref, amount_minor, currency, occurred_at, recipient, status, source_document_id, idempotency_key)`
- `commissions(id, merchant_id, basis_type, basis_id, rate, amount_minor, currency, recognition_at, settlement_mode, status, idempotency_key)`
- `admin_injections(id, person_id, injection_type, amount_minor, currency, occurred_at, status, idempotency_key)`
- `card_transfers(id, source_card_id, destination_card_id, amount_minor, currency, occurred_at, status, idempotency_key)`
- `outsource_operations(id, client_id, acceptance_party_id, agent_id, gross_minor, buy_rate, sell_rate, agent_rate, occurred_at, status)`

## Ledger

- `accounts(id, code, name, account_type, status)`
- `journal_entries(id, event_type, event_id, occurred_at, posted_at, created_by, currency, status, reversal_of_id, idempotency_key)`
- `postings(id, journal_entry_id, account_id, side, amount_minor, merchant_id, card_id, custodian_id, category_id, source_document_id)`

Ограничения:

- `amount_minor > 0`;
- уникальный `journal_entries.idempotency_key`;
- уникальная пара `(event_type, event_id)` для обычной публикации;
- стороны только `debit`/`credit`;
- баланс entry проверяется внутри транзакционного posting API;
- posted rows не обновляются и не удаляются приложением.

## Сверка и opening

- `reconciliation_sessions(id, scope_type, scope_id, as_of, status, created_by, closed_by)`
- `reconciliation_items(id, session_id, expected_minor, observed_minor, difference_minor, status, resolution_event_type, resolution_event_id)`
- `opening_balance_batches(id, cutover_at, source_document_id, status, approved_by, posted_entry_id)`

## Индексы первого порядка

- события по `occurred_at`;
- postings по account и каждому dimension;
- незакрытые документы по status;
- source documents по checksum/external ID;
- import rows по batch и row number;
- Telegram updates по внешнему update ID;
- idempotency keys уникальны.
