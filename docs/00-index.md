# База знаний «Металлист»

Статус: первоначальная версия
Дата: 2026-09-11

Этот каталог — канонический источник продуктовой, финансовой и технической логики. Документы разделяют подтверждённые факты, принятые архитектурные решения, проектные предложения и открытые вопросы.

## Статусы утверждений

- **Подтверждено** — прямо описано владельцем процесса или однозначно следует из источника.
- **Принято** — выбранное архитектурное решение, зафиксированное ADR.
- **Проект** — рабочее предложение, которое можно уточнять до реализации.
- **Открытый вопрос** — решение отсутствует; влияющее на деньги поведение не реализуется до ответа.

## Навигация

### Контекст и источники

- [Статусы и источники](status-and-sources.md)
- [Глоссарий](glossary.md)
- [Открытые вопросы](open-questions.md)
- [Дорожная карта](roadmap.md)

### AS IS

- [Фактический бизнес-процесс](as-is/business-process.md)
- [Роли и ответственность](as-is/roles-and-responsibilities.md)
- [Модель книги «Мотобелки»](as-is/spreadsheet-model.md)
- [Форматы входных данных](as-is/source-data-formats.md)

### TO BE

- [Целевой процесс](to-be/target-process.md)
- [Жизненные циклы операций](to-be/operation-lifecycle.md)
- [Модель доступа](to-be/permissions.md)
- [Карты и лимиты пополнений](to-be/cards-and-limits.md)

### Управленческий учёт

- [Принципы](accounting/principles.md)
- [План счетов](accounting/chart-of-accounts.md)
- [Правила проводок](accounting/posting-rules.md)
- [Изменение тарифа задним числом](accounting/retroactive-tariff-recalculation.md)
- [Начальные остатки](accounting/opening-balances.md)
- [Сверка](accounting/reconciliation.md)

### Архитектура

- [Контекст системы](architecture/system-context.md)
- [Доменная модель](architecture/domain-model.md)
- [Модули](architecture/modules.md)
- [Предварительная модель БД](architecture/database.md)

### Интеграции

- [Telegram](integrations/telegram.md)
- [Импорт XLSX](integrations/xlsx-import.md)
- [Объектное хранилище](integrations/object-storage.md)
- [Команды приложения](api/commands.md)

### Эксплуатация и безопасность

- [Окружения](operations/environments.md)
- [Развёртывание](operations/deployment.md)
- [Первая демонстрационная версия](operations/demo-v1.md)
- [Миграция запросов карт к оплате](operations/payment-request-migration.md)
- [Миграция Telegram-сборщиков](operations/telegram-collector-migration.md)
- [Миграция профилей импорта мерчантов](operations/merchant-import-profiles-migration.md)
- [Миграция ФИО и телефонов запросов карт](operations/payment-contact-migration.md)
- [Резервное копирование](operations/backups.md)
- [Секреты и чувствительные данные](security/secrets-and-sensitive-data.md)
- [Границы доверия](security/trust-boundaries.md)
- [Аудит операций](security/audit-log.md)

### Проверка качества

- [Приёмочные сценарии](testing/acceptance-scenarios.md)
- [Финансовые инварианты](testing/accounting-invariants.md)

### Принятые решения

- [ADR-0001: Modular monolith](decisions/ADR-0001-modular-monolith.md)
- [ADR-0002: Double-entry ledger](decisions/ADR-0002-double-entry-ledger.md)
- [ADR-0003: Docs as code](decisions/ADR-0003-docs-as-code.md)
- [ADR-0004: Source → Business → Ledger](decisions/ADR-0004-three-layer-model.md)
