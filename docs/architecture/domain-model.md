# Доменная модель

Статус: проект
Дата: 2026-09-11

## Участники и справочники

- `User`: пользователь системы и его статус.
- `Role`: набор разрешений.
- `Merchant`: мерчант и параметры расчётов.
- `Bank`: нормализованный банк.
- `Card`: внутренний ID карты, банк, отображаемые последние цифры, статус.
- `Custodian`: лицо или место, ответственное за деньги.
- `ExpenseCategory`: управляемая категория расхода.

## Документы и группировки

- `MaterialBatch`: набор карт, предоставленный мерчанту.
- `SourceDocument`: оригинальный файл, сообщение или ручной отчёт.
- `ImportBatch` и `ImportRow`: результат версии parser.
- `MerchantRegistry` и `MerchantRegistryItem`: бизнес-документ мерчанта.
- `Attachment`: ссылка на защищённый объект.

## События

- `MerchantFunding`;
- `Withdrawal`;
- `CardBalanceObservation`;
- `CashHandover`;
- `MerchantRepayment`;
- `Expense`;
- `CommissionAccrual`;
- `AdminInjection`;
- `CardTransfer`;
- `OutsourceOperation`;
- `OpeningBalanceBatch`.

События независимы. Не создаётся один огромный объект со статусом `FUNDED → WITHDRAWN → RETURNED`, потому что суммы разных пополнений могут смешиваться и сниматься частями.

## Учёт и контроль

- `Account`;
- `JournalEntry`;
- `Posting`;
- `ReconciliationSession`;
- `ReconciliationItem`;
- `AuditEvent`.

## Общие поля события

- `id`;
- `occurred_at`;
- `recorded_at`;
- `created_by`;
- `performed_by`, если отличается;
- `source_document_id`;
- `status`;
- `amount_minor` и `currency`;
- `idempotency_key`;
- `confirmed_by`, `confirmed_at`;
- `reversal_of_id` и причина, если применимо.
