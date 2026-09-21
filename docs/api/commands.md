# Команды приложения

Статус: проект
Дата: 2026-09-11

Web, Telegram и import workers вызывают одинаковые application commands.

## Первоначальный каталог

- `CreateMerchant`
- `RegisterCard`
- `CreatePaymentContact` (ФИО и телефон в справочнике; в demo только синтетические значения)
- `CreatePaymentRequest` (плановые строки с обязательным `contact_id` и снимком ФИО/телефона; без ledger)
- `ExportPaymentRequestXLSX` (повторная выдача ранее сформированного файла после проверки SHA-256)
- `SetCardLimit`
- `CreateMaterialBatch`
- `UploadRegistry`
- `SetRegistryOneOffRate` (проектное имя: главный администратор назначает и подтверждает разовую ставку уже загруженному реестру до подтверждения поступлений; обновляет preview, не ledger)
- `PreviewRegistryManualRateCorrection` и `ConfirmRegistryManualRateCorrection` (проектные имена: исправление ручной ставки уже подтверждённого реестра; первая команда показывает финансовую разницу и баланс до/после без postings, вторая после явного подтверждения главного администратора создаёт связанные корректирующие postings)
- `ApproveRegistry`
- `ApplyRegistry`
- `ConfirmMerchantFunding`
- `RecordWithdrawal`
- `RecordCardBalanceObservation`
- `CreateCashHandover`
- `ConfirmCashHandover`
- `RecordMerchantRepayment`
- `ConfirmCashSurplusResolution` (проектное имя: главный администратор классифицирует подтверждённый излишек; для денег мерчанта переводит невыясненное обязательство в отдельный долг этому мерчанту, не списывая наличные)
- `ConfirmMerchantSurplusReturn` (проектное имя: подтверждает отдельную фактическую выдачу найденных денег мерчанту; списывает наличные и погашает только долг по связанному излишку, не обычную кредиторку по реестрам)
- `RecordExpense`
- `ApproveExpense`
- `AccrueCommission`
- `PreviewRetroactiveTariffRecalculation` (проектный command; без публикации ledger)
- `ConfirmRetroactiveTariffRecalculation` (проектный command; явное подтверждение главным администратором и корректирующие postings)
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

Для новых обычных расходов кабинет передаёт категории `agent_fee`, `bank_fee`, `salary`, `warmup`, `it_infrastructure`, `taxes`, `communication`, `delivery`, `other`. Сервер не принимает `repayment`, `losses` и `dividends` как категорию `RecordExpense`: первая относится к `RecordMerchantRepayment`, вторая — к отдельному списанию подтверждённой недостачи, третья ожидает самостоятельного правила распределения капитала. Старые категории `operating` и `transport` читаются и могут завершаться для уже подготовленных черновиков.

`GET /api/catalog` для главного администратора и операциониста включает `request_cards`: активные карты в порядке распределения строк с `bank` и `full_name`/`phone` из последнего снимка реестра по карте. Поля независимы в форме; перед формированием XLSX новые комбинации сохраняются через `payment_contact`. Повторная комбинация возвращает существующий активный контакт без UPDATE и без расширения прав прикладной роли.

`POST /api/payment-request/create` для нового ручного запроса принимает `mode: "manual"` и массив `rows` с `card_id`, `amount` и `contact_id` в каждой строке. Сервер проверяет активность карты и контакта, положительную точную сумму, от 1 до 500 строк и уникальность `card_id` внутри запроса. Автозаполнение в интерфейсе сначала строит редактируемые строки, затем передаёт этот же явный состав. Старые режимы API `count`/`total` сохраняются, но не могут использовать одну карту повторно в запросе.
