# Модули приложения

Статус: проект
Дата: 2026-09-11

| Модуль | Ответственность |
|---|---|
| identity | пользователи, роли, Telegram identity, сессии |
| merchants | мерчанты и условия взаимодействия |
| cards | карты, банки, статусы, назначения и лимиты |
| materials | наборы карт и их передача мерчанту |
| registries | исходные реестры, import batches и строки |
| funding | подтверждённые пополнения карт |
| withdrawals | снятия и наблюдения остатков |
| cash | хранители, передачи и деньги в пути |
| settlements | возвраты мерчантам и взаиморасчёты |
| commissions | правила и начисления комиссии |
| expenses | расходы, категории, получатели и approvals |
| outsource | операции с клиентом, приёмкой и агентом |
| ledger | accounts, journal entries, postings и balances |
| reconciliation | фактические наблюдения и расхождения |
| reporting | Cashflow, P&L, balance и debt reports |
| documents | S3 references, checksums и retention |
| telegram | входящие updates, команды и ответы |
| audit | история значимых действий |

## Зависимости

Бизнес-модули вызывают публичный интерфейс ledger внутри одной PostgreSQL transaction. Ledger не зависит от Telegram, React, XLSX и конкретных workflow-экранов.

Reporting читает ledger и бизнес-документы, но не изменяет их. Reconciliation может предложить исправление, но не публикует его без отдельной команды.
