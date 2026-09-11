# Окружения

Статус: проект с подтверждённым серверным baseline  
Дата: 2026-09-11

## Local

- локальные контейнеры;
- синтетические данные;
- отдельная disposable PostgreSQL;
- локальные или тестовые интеграционные credentials.

## Staging

- домен `metallcash.work` временно направлен на подготовленный сервер;
- HTTPS обслуживает Caddy;
- вход на сервер только `deploy` по SSH-ключу;
- root login и password authentication отключены;
- UFW разрешает SSH, HTTP и HTTPS;
- рабочий каталог `/opt/metallist-platform`;
- Telegram token хранится вне репозитория в закрытом системном файле.

Приложение, PostgreSQL, container runtime, CI/CD и backups пока не развёрнуты.

## Production

Production ещё не создан. Он должен иметь отдельные database, secrets, object storage namespace и Telegram-конфигурацию. Нельзя переносить staging-данные в production без контролируемой миграции.

## Правило безопасности тестов

Перед любой database-capable проверкой нужно доказать, что target является выделенной disposable test database. Название переменной `TEST_DATABASE_URL` само по себе доказательством не является.
