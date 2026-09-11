# Развёртывание

Статус: проект  
Дата: 2026-09-11

## Текущее состояние

- GitHub repository: `anr65/metallist-platform`;
- сервер доступен по ключу пользователю `deploy`;
- Caddy выдаёт сертификат и временный ответ на `https://metallcash.work`;
- ports 80/443 готовы для reverse proxy;
- repository на сервер ещё не подключён;
- приложения и БД ещё нет.

## Предлагаемый deployment flow

1. Pull request проходит CI.
2. Merge в `main` создаёт immutable application image.
3. Staging получает image по точной версии/commit SHA.
4. Выполняется безопасная миграция staging DB.
5. Проходят smoke tests и release gate.
6. Production deployment требует отдельного решения и backup prerequisite.

## Ближайшие инфраструктурные задачи

- создать серверный GitHub deploy key или CI deployment identity;
- выбрать container runtime;
- создать staging PostgreSQL без публичного порта;
- подключить Caddy как reverse proxy к приложению;
- настроить health endpoint;
- настроить offsite backups;
- добавить monitoring и alerting;
- определить production environment.

## Запреты

- ручной copy исходников как постоянный deployment;
- secrets в GitHub repository или image;
- `latest` без зафиксированного digest/version;
- миграция production до backup и проверки плана;
- сообщение о successful release без проверенных evidence.
