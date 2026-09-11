# Предварительный план счетов

Статус: проект
Дата: 2026-09-11

План счётов должен оставаться коротким. Конкретные карты, мерчанты и ответственные хранятся как dimensions.

| Код | Наименование | Тип | Основное измерение |
|---|---|---|---|
| 1100 | Деньги на картах | asset | card_id |
| 1200 | Наличные у операционистов/сборщиков | asset | custodian_id |
| 1210 | Наличные у главного администратора | asset | custodian_id |
| 1220 | Наличные в пути | asset | transfer_id |
| 1300 | Дебиторка мерчантов по комиссии | asset | merchant_id |
| 1390 | Прочая дебиторка | asset | counterparty_id |
| 2100 | Долг перед мерчантами | liability | merchant_id |
| 2200 | Долг перед главным администратором | liability | person_id |
| 2290 | Прочая кредиторка | liability | counterparty_id |
| 3100 | Opening balance / капитал перехода | equity | opening_batch_id |
| 4100 | Комиссионная выручка | revenue | merchant_id, commission_type |
| 4200 | Прочая выручка | revenue | category_id |
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
- разделять ли наличные сборщика и операциониста разными счетами;
- считать ли отдельные инъекции liability или equity;
- нужен ли отдельный clearing-счёт для неподтверждённых переводов.
