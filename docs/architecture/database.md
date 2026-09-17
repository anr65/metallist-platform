# Предварительная модель PostgreSQL

Статус: проект
Дата: 2026-09-11

Точные типы, ограничения и имена уточняются перед первой миграцией. Все денежные поля предполагаются как точный PostgreSQL `DECIMAL(20,2)` в рублях и копейках, без `float`; это проектный выбор типа и разрядности на основании согласованной точности. Процентная ставка хранится в отдельном точном `DECIMAL` с двумя знаками после запятой; общая разрядность и допустимый диапазон уточняются перед миграцией. Идентификаторы — UUID или другой единый непрогнозируемый тип.

## Identity и аудит

- `users(id, display_name, status, created_at)`
- `roles(id, code)`
- `user_roles(user_id, role_id)`
- `telegram_identities(user_id, telegram_user_id, status)`
- `audit_events(id, recorded_at, actor_user_id, actor_role, channel, action, object_type, object_id, old_status, new_status, outcome, reason_code, request_id, business_event_id, source_document_id, journal_entry_id, safe_diff, metadata)`; append-only, правила в [аудите операций](../security/audit-log.md)

## Справочники

- `merchants(id, code, name, status, created_at)`
- `merchant_tariffs(id, merchant_id, version, percent_rate, valid_from, valid_to, status, created_by, created_at)`; `valid_from` — **включённая** календарная дата в `Europe/Moscow`: реестр, подтверждённый в этот день, уже получает новую ставку; `percent_rate` — точный `DECIMAL(…,2)` в процентах, например `4,25 %`; менять ставку вправе только главный администратор, с записью аудита; `created_by/at` и версионирование — техническое предложение для сохранения истории; денежный результат комиссии округляется по `ROUND_HALF_UP`, включая `49,3850 → 49,39`
- `tariff_recalculations(id, merchant_id, old_tariff_id, new_tariff_id, effective_from, status, preview_snapshot, before_balances, after_balances, requested_by, previewed_at, confirmed_by, confirmed_at, idempotency_key)`; `effective_from` — дата начала ставки, которую главный администратор указывает при смене тарифа. Проектная сущность отбирает **не сторнированные обычные** реестры по дате их `confirmed_at`, включая реестры после уже существовавших более поздних дат смены ставки; реестры с ручной разовой ставкой исключает из корректировок и явно показывает как исключённые в preview. Пересчёт требует явного подтверждения. После подтверждения новая ставка применяется к будущим обычным реестрам, а вытесненные ставки сохраняются для аудита, но не выбираются для новых операций. Точная модель версий — проект
- `tariff_recalculation_items(id, recalculation_id, registry_id, old_commission_amount, new_commission_amount, old_net_payable_amount, new_net_payable_amount, adjustment_entry_id)`; проектная связь со всеми затронутыми реестрами и новыми корректирующими записями. При последующем откате реестра эти связи позволяют автоматически найти и сторнировать **все** его тарифные корректировки без удаления исходных записей
- Для исправления ручной ставки после подтверждения реестра нужна отдельная от изменения общего тарифа версия решения с `registry_id`, прежней/новой ставкой и суммами, preview баланса до/после, причиной, автором и временем подтверждения, идемпотентным ключом и ссылками на новые корректирующие `journal_entries`. Конкретная таблица — технический проект; исходное одобрение и postings сохраняются. Для P&L корректирующая запись относится к `confirmed_at` исходного реестра по `Europe/Moscow`, а фактическое время публикации хранится отдельно; при затронутом утверждённом отчёте требуется новая версия и повторное утверждение бухгалтером. При последующем откате реестра необходимо учитывать и эти корректировки, чтобы итоговые комиссия и долг отменённого реестра стали нулевыми.
- `banks(id, code, name)`
- `cards(id, bank_id, last4, mask, pan_vault_ref, owner_full_name_ref, status, created_at, retired_at)`; полный PAN вне основной таблицы, ФИО — отдельно защищённое персональное поле/ссылка, точная схема защищённого хранения до production открыта
- `card_limits(id, card_id, limit_type, period_type, count_limit nullable, amount_limit nullable, currency, merchant_id nullable, valid_from, valid_to)`; четыре согласованных вида ограничения перечислены в [правиле лимитов](../to-be/cards-and-limits.md), конкретные пороги и окна частично открыты
- `custodians(id, type, user_id nullable, name, status)`; отдельная запись для каждого сборщика, даже если операции за него вводит операционист
- `expense_categories(id, code, name, status)`

Полный PAN требуется в бизнес-карточке, но не является открытым полем основной таблицы `cards`: используется защищённое хранилище по ссылке. PIN не сохраняется нигде.

## Документы и импорт

В демонстрационной схеме 002 есть `payment_requests(id, merchant_id, external_ref, mode, payment_count, per_payment_cents, requested_total_cents, export_path, export_sha256, created_by, created_at)` и `payment_request_rows(id, request_id, row_no, card_id, planned_cents, synthetic_number, synthetic_name)`. Пара `(merchant_id, external_ref)` и номер строки в запросе уникальны. `registries.payment_request_id` необязателен; ответный реестр проверяет совпадение мерчанта. Запрос хранит неизменяемый план и контрольную сумму файла, не создаёт проводок. `synthetic_number` — только демонстрационный недействительный номер; production PAN и ФИО здесь хранить запрещено до отдельной архитектуры защищённого хранилища.

Схема 004 добавляет `registry_parser_types(code, name, version, accepted_extensions, active)` и ровно один `merchant_import_profiles(merchant_id, parser_code, status, updated_at)` на мерчанта. Статус `configured` требует parser, `awaiting_sample` запрещает его. `source_documents.parser_code/parser_version` фиксируют реально применённую версию; последующее изменение профиля не меняет старый документ. Таблицы не создают проводок и не содержат содержимое исходного файла.

- `source_documents(id, source_type, external_id, checksum, storage_key, received_at, received_by, metadata)`
- `import_batches(id, source_document_id, parser_version, status, created_at, approved_by, confirmed_at, applied_at, reversed_by, reversed_at, reversal_reason)`; срок отката считается от `confirmed_at`
- `import_rows(id, batch_id, sheet_name nullable, row_number, raw_data, normalized_data, source_order_ref nullable, source_rrn nullable, status, error_codes)`; для CSV `sheet_name` пуст, порядковый номер/номер заказа/RRN сохраняются как данные источника, но их глобальная уникальность пока не доказана
- `merchant_registries(id, merchant_id, source_document_id, external_ref, occurred_at, confirmed_at, status, total_amount, commission_amount, net_payable_amount, tariff_id, tariff_snapshot, currency)`; календарная дата `confirmed_at` в `Europe/Moscow` определяет попадание под тариф с указанной прошлой датой, не `occurred_at` и не дата загрузки. `commission_amount` — процент по тарифу от `total_amount`, итог сохраняется до копейки, `net_payable_amount = total_amount − commission_amount`; для незавершённого реестра после изменения ставки пересчитывается preview по новой ставке. Снимок тарифа фиксируется при подтверждении (техническое предложение); назначение тарифа задним числом корректирует историю **новыми** ledger-записями, не меняя исходные postings
- Для одного уже загруженного реестра возможна разовая процентная ставка вместо общего `merchant_tariffs.percent_rate`. В интерфейсе главный администратор назначает и подтверждает её **после загрузки и до отдельного подтверждения поступлений им же**. Проектная схема связывает исключение с конкретным `merchant_registries.id` и сохраняет применённую ставку, исходную общую ставку, основание договорённости, `approved_by`/`approved_at` главного администратора и факт разового применения. Нужен явный признак источника ставки: минимальный вариант — `merchant_registries.is_manual_rate BOOLEAN NOT NULL DEFAULT FALSE`; при `TRUE` должны быть заполнены разовая ставка, основание и одобрение главного администратора. Такой реестр не попадает в корректировки при ретроактивном изменении общего тарифа. Не менять строку общего тарифа ради одного реестра; сущность для предварительного резервирования на неизвестный «следующий» реестр не нужна. Изменение ставки инвалидирует старый preview, но не создаёт ledger posting до отдельного подтверждения факта поступления.
- `merchant_registry_items(id, registry_id, row_id, card_id, amount, currency, external_ref, status)`

## Операционные события

Добавочная демонстрационная схема 003 вводит роль `collector`, уникальную связь `users.custodian_id` с фактическим хранителем, сохраняет очищенные метаданные Telegram update в `telegram_updates.raw_update` и краткоживущий выбор команды в `telegram_dialogs`. Произвольный текст чата не сохраняется в этой таблице; принятая валидная команда остаётся в черновике. Подтверждение кнопкой использует существующий `drafts` и неизменяемый `journal_entries`/`postings`; отдельного редактируемого поля баланса не появляется. Ключ `draft:<draft_id>` обеспечивает однократную публикацию, а `telegram:<update_id>` — однократный черновик источника. Полный номер карты в демо не сохраняется.

- `merchant_fundings(id, merchant_id, card_id, registry_item_id, amount, currency, occurred_at, status, idempotency_key)`
- `withdrawals(id, card_id, custodian_id, amount, currency, occurred_at, status, source_document_id, idempotency_key)`; `custodian_id` — фактический получатель наличных, обязателен при подтверждении
- `card_balance_observations(id, card_id, observed_amount, currency, observed_at, reporter_id, source_document_id)`
- `cash_handovers(id, from_custodian_id, to_custodian_id, claimed_amount, confirmed_amount, currency, occurred_at, status, confirmed_by, confirmed_at, idempotency_key)`; для передачи сборщик → главный администратор `confirmed_by` — пользователь самого главного администратора, ledger использует только `confirmed_amount`
- В первом релизе физическая доставка через другого сборщика не создаёт строки `cash_handovers` для промежуточной передачи и не меняет `custodian_id` на посредника. Одна итоговая строка связывает исходного сборщика с главным администратором и фактически принятой суммой. Дополнительная сущность маршрута/курьера сейчас не требуется; недостача при потере относится на исходного сборщика А после отдельного подтверждения
- `cash_shortages(id, custodian_id, amount, currency, source_handover_id, reason, status, confirmed_by, confirmed_at, idempotency_key)`; по сборщику подтверждает главный администратор, публикация переносит сумму из наличных в его дебиторку
- `shortage_writeoffs(id, cash_shortage_id, person_id, amount, currency, reason, status, approved_by, approved_at, idempotency_key)`; утверждает главный администратор, публикация уменьшает дебиторку и признаёт расход
- `cash_surpluses(id, custodian_id, amount, currency, source_observation_id, status, confirmed_by, confirmed_at, idempotency_key)`; для первого запуска подтверждает главный администратор, одно подтверждение создаёт одну проводку в наличные и невыясненное поступление
- `cash_surplus_resolutions(id, cash_surplus_id, amount, resolution_type, merchant_id nullable, source_document_id, reason, approved_by, approved_at, idempotency_key)`; утверждает главный администратор. При `resolution_type = merchant_money` обязателен конкретный `merchant_id`; подтверждение переводит сумму из `2300` в отдельное обязательство этому мерчанту, но не списывает наличные и не уменьшает обычный долг по реестрам. Идентификатор resolution служит измерением отдельного долга, чтобы последующая выдача погасила именно его
- Для `resolution_type = collector_shortage_repayment` требуется `shortage_id` и совпадение фактического сборщика-должника; сумму разрешено проводить лишь в пределах открытых остатков исходного излишка и недостачи. Это проектное расширение `cash_surplus_resolutions` (ссылка `shortage_id nullable`): при подтверждении оно закрывает `2300` против дебиторки, не создавая второй записи прихода денег и дохода. Для уже списанного долга нужен отдельный тип «возврат после списания» со ссылкой на write-off; ошибочная исходная недостача исправляется через reversal/correction, а не этим типом
- `merchant_repayments(id, merchant_id, from_custodian_id, amount, currency, purpose, surplus_resolution_id nullable, occurred_at, status, idempotency_key)`; обычный возврат относится к мерчанту в целом, `registry_id` не требуется. Для `purpose = surplus_return` обязателен `surplus_resolution_id` того же мерчанта; подтверждение списывает наличные и погашает только ещё открытый остаток отдельного обязательства по этому resolution, без зачёта обычной кредиторки. Имена полей и enum — проект
- `expenses(id, category_id, source_account_ref, amount, currency, occurred_at, recipient, status, source_document_id, idempotency_key)`
- `commissions(id, merchant_id, basis_type, basis_id, rate, amount, currency, recognition_at, settlement_mode, status, idempotency_key)`
- `admin_injections(id, person_id, injection_type, amount, currency, occurred_at, status, idempotency_key)`
- `receivable_collections(id, debtor_type, debtor_id, receivable_ref, writeoff_ref nullable, destination_account_ref, amount, currency, occurred_at, confirmed_by, confirmed_at, status, source_document_id, idempotency_key)`; проектная сущность для фактического возврата по ранее учтённой дебиторке с указанием должника, конкретного долга и места получения денег. Для ранее списанной части обязательна связь со списанием; её подтверждённое получение признаётся прочим доходом, не восстанавливает исходный расход. Не смешивается с `admin_injections`
- `card_transfers(id, source_card_id, destination_card_id, amount, currency, occurred_at, status, idempotency_key)`
- `outsource_operations(id, client_id, acceptance_party_id, agent_id, gross_amount, buy_rate, sell_rate, agent_rate, occurred_at, status)`

## Ledger

- `accounts(id, code, name, account_type, status)`
- `journal_entries(id, event_type, event_id, occurred_at, recognition_at, posted_at, created_by, currency, status, reversal_of_id, idempotency_key)`; `recognition_at` — предложенное отдельное время отнесения к управленческому P&L. Для ретроактивной корректировки комиссии оно соответствует `confirmed_at` исходного реестра, тогда как `posted_at` фиксирует реальный день публикации; исходные entries не меняются
- `postings(id, journal_entry_id, account_id, side, amount, merchant_id, card_id, custodian_id, category_id, source_document_id)`

Ограничения:

- `amount > 0`, все денежные поля имеют точность два знака после запятой;
- уникальный `journal_entries.idempotency_key`;
- уникальная пара `(event_type, event_id)` для обычной публикации;
- стороны только `debit`/`credit`;
- баланс entry проверяется внутри транзакционного posting API;
- posted rows не обновляются и не удаляются приложением.

## Сверка и opening

- `reconciliation_sessions(id, scope_type, scope_id, as_of, status, created_by, closed_by)`
- `reconciliation_items(id, session_id, expected_amount, observed_amount, difference_amount, status, resolution_event_type, resolution_event_id)`
- `opening_balance_batches(id, cutover_at, source_document_id, status, approved_by, posted_entry_id)`

## Утверждение управленческих отчётов

Подтверждённый главным администратором ретроактивный перерасчёт тарифа может изменить показатели даже формально закрытого периода с отчётом, уже утверждённым бухгалтером. После перерасчёта новая версия отчёта требует **повторного утверждения бухгалтером**; до этого новые суммы доступны, но не утверждены. Хранить только изменяемый флаг `approved` у периода недостаточно: нужны неизменяемые свидетельства того, **какие суммы, кем и когда** были утверждены, а также связь с последующими корректировками. Техническое предложение — отдельные версии отчёта и append-only история утверждений с идентификатором версии, снимком сумм, `approved_by` и `approved_at`; новая версия не наследует approval прежней. Детали хранения снимков уточняются. Текущий P&L по-прежнему рассчитывается из ledger с учётом `recognition_at`, а не подменяется сохранённым снимком.

## Индексы первого порядка

- события по `occurred_at`;
- postings по account и каждому dimension;
- незакрытые документы по status;
- source documents по checksum/external ID;
- import rows по batch и row number;
- Telegram updates по внешнему update ID;
- idempotency keys уникальны.
- audit events по `(object_type, object_id, recorded_at)` и `request_id`; журнал не обновляется и не удаляется через прикладную роль.
