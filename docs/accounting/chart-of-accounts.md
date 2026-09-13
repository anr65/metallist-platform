# Предварительный план счетов

Статус: проект кодов, согласованные типы остатков
Дата обновления: 2026-09-13

План счётов должен оставаться коротким. Конкретные карты, мерчанты и ответственные хранятся как dimensions.

| Код | Наименование | Тип | Основное измерение |
|---|---|---|---|
| 1100 | Деньги на картах | asset | card_id |
| 1200 | Наличные у сборщиков и других фактических хранителей | asset | custodian_id каждого лица |
| 1210 | Наличные у главного администратора | asset | custodian_id |
| 1220 | Наличные в пути | asset | transfer_id |
| 1300 | Дебиторка мерчанта по переплате | asset | merchant_id |
| 1390 | Прочая дебиторка | asset | counterparty_id |
| 1400 | Дебиторка ответственного по недостаче | asset | person_id |
| 2100 | Долг перед мерчантами | liability | merchant_id |
| 2110 | Отдельный долг мерчанту по выясненному кассовому излишку | liability | merchant_id, surplus_resolution_id |
| 2200 | Долг перед вносившим собственные средства | liability | person_id |
| 2300 | Невыясненные поступления | liability | source_id |
| 2290 | Прочая кредиторка | liability | counterparty_id |
| 3100 | Opening balance / капитал перехода | equity | opening_batch_id |
| 3200 | Капитал от прощения долга | equity | person_id, decision_id |
| 4100 | Комиссионная выручка | revenue | merchant_id, commission_type |
| 4200 | Прочие доходы | revenue | category_id |
| 5100 | Операционные расходы | expense | expense_category_id |
| 5200 | Агентские расходы | expense | agent_id |
| 5300 | Банковские комиссии | expense | bank_id |
| 5400 | Потери и недостачи | expense | responsible_id |
| 5900 | Прочие расходы | expense | expense_category_id |

## Обязательные dimensions

- merchant_id;
- card_id;
- custodian_id;
- source_document_id;
- business_event_id;
- material_batch_id при наличии;
- expense_category_id или commission_type, когда применимо.

## Открытые решения

- нужен ли отдельный счёт для заблокированных денег или достаточно статуса/dimension;
- нужны ли для каких-либо видов хранителей отдельные коды счетов сверх аналитики `custodian_id`;
- условия окончательной переклассификации возвратных средств в капитал;
- нужен ли отдельный clearing-счёт для неподтверждённых переводов.
