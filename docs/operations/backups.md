# Резервное копирование

Статус: проект; ещё не реализовано
Дата: 2026-09-11

## Объекты

- PostgreSQL;
- S3 documents и metadata;
- application/deployment configuration без секретов;
- encrypted secrets backup по отдельной процедуре;
- audit и migration history.

## Минимальная политика

- ежедневный logical или physical backup PostgreSQL;
- непрерывное архивирование WAL/PITR, если БД self-hosted;
- offsite storage отдельно от VPS;
- шифрование при передаче и хранении;
- retention с несколькими временными уровнями;
- monitoring последнего успешного backup;
- регулярное восстановление в изолированное окружение.

Backup считается работоспособным только после проверенного restore. RPO/RTO определяются в INFRA-002.

## Restore drill

1. Развернуть отдельную пустую БД.
2. Восстановить backup или состояние на выбранный момент.
3. Проверить migrations, ledger balance и ключевые отчёты.
4. Сопоставить количество documents и checksums.
5. Зафиксировать время, ошибки и фактический RPO/RTO.
6. Уничтожить тестовое окружение безопасно.
