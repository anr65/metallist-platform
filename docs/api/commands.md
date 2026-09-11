# Команды приложения

Статус: проект
Дата: 2026-09-11

Web, Telegram и import workers вызывают одинаковые application commands.

## Первоначальный каталог

- `CreateMerchant`
- `RegisterCard`
- `SetCardLimit`
- `CreateMaterialBatch`
- `UploadRegistry`
- `ApproveRegistry`
- `ApplyRegistry`
- `ConfirmMerchantFunding`
- `RecordWithdrawal`
- `RecordCardBalanceObservation`
- `CreateCashHandover`
- `ConfirmCashHandover`
- `RecordMerchantRepayment`
- `RecordExpense`
- `ApproveExpense`
- `AccrueCommission`
- `RecordAdminInjection`
- `RecordCardTransfer`
- `ReverseEvent`
- `OpenReconciliation`
- `ResolveReconciliationItem`
- `PostOpeningBalanceBatch`

## Общий command envelope

- command_id / idempotency_key;
- actor_user_id;
- occurred_at;
- source_document_id;
- payload;
- reason/comment;
- expected version для конкурентных изменений.

Command handler валидирует права и состояние, сохраняет business event и вызывает ledger в одной транзакции, когда событие должно быть опубликовано.
