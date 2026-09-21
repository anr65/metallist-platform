# Развёртывание

Статус: действующее production-окружение с историческим именем demo
Дата обновления: 2026-09-21

По решению владельца процесса ветка `demo-v1` и база `metallist_demo` на хосте являются production. Ниже сохранены исторические сведения о первоначальном развертывании; утверждения о синтетических данных и отсутствии production более не действуют.

## Текущее состояние

- GitHub repository: `anr65/metallist-platform`;
- сервер доступен по ключу пользователю `deploy`;
- Caddy обслуживает `https://metallcash.work` и проксирует приложение на `127.0.0.1:8080`;
- `metallist-demo.service` работает из `/opt/metallist-platform/current`, точная версия — commit ветки `demo-v1`;
- PostgreSQL содержит рабочую production-базу `metallist_demo`; приложение подключено отдельной малопривилегированной ролью;
- перед первичным применением схемы был создан backup `metallist_demo_before_initial_20260913T191223Z.dump` и успешно восстановлен в отдельную `metallist_demo_restore_test`;
- перед добавочной миграцией 002 создан закрытый backup `/var/backups/metallist-platform/metallist_demo_before_payment_requests_20260914T071019Z.dump` (62 092 байта, права `0600`), проверен `pg_restore -l` и восстановлен в пустую `metallist_demo_restore_test`; совпали 2 пользователя, 3 реестра, 10 journal entries, 26 postings и нулевой дисбаланс. На восстановленной копии миграция 1→2 прошла; в `metallist_demo` после неё остались 10 entries, 26 postings и нулевой дисбаланс;
- отдельная локальная одноразовая база `metallist_platform_test` используется для автоматических проверок. Production-база `metallist_demo` не используется для тестовых сбросов и экспериментов с миграциями.

## Текущий production deployment flow

Код из production-ветки `demo-v1` собирается на VPS из точного commit SHA в отдельный каталог release, после чего атомарно обновляется ссылка `/opt/metallist-platform/current` и перезапускается systemd-сервис. Схема не мигрирует при обновлении приложения. Конфигурация и секреты находятся вне Git, в `/etc/metallist-platform/`. Перед изменением схемы `metallist_demo` требуется новая точная проверка цели и проверенное восстановление свежей копии.

## Ранее предложенный целевой flow (ещё не внедрён)

1. Pull request проходит CI.
2. Merge в `main` создаёт immutable application image.
3. Staging получает image по точной версии/commit SHA.
4. Выполняется безопасная миграция staging DB.
5. Проходят smoke tests и release gate.
6. Production deployment требует отдельного решения и backup prerequisite.

## Ближайшие инфраструктурные задачи

- создать CI deployment identity и pipeline;
- оценить необходимость переименования production PostgreSQL и конфигурации без потери данных;
- настроить offsite backups;
- добавить monitoring и alerting;
- поддерживать актуальную инвентаризацию production environment.

## Запреты

- ручной copy исходников как постоянный deployment;
- secrets в GitHub repository или image;
- `latest` без зафиксированного digest/version;
- миграция production до backup и проверки плана;
- сообщение о successful release без проверенных evidence.
