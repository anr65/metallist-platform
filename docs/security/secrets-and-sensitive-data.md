# Секреты и чувствительные данные

Статус: принято  
Дата: 2026-09-11

## Запрещено хранить в Git

- Telegram bot token;
- SSH private keys;
- пароли сервера и БД;
- S3 access keys;
- cookie/session secrets;
- PIN карт;
- production dumps;
- неанонимизированные реестры и Telegram exports;
- полные карточные данные без отдельного утверждённого режима.

## Разрешённые references

- имя переменной, например `TELEGRAM_BOT_TOKEN`;
- bot username;
- secret storage path без значения;
- public key;
- checksum исходного файла;
- обезличенные fixtures.

## Правила

- отдельные secrets для local, staging и production;
- минимальные права;
- rotation после раскрытия или смены участника;
- секрет не выводится в log/error;
- config endpoint не возвращает секреты;
- токен, ранее опубликованный в переписке, должен быть перевыпущен до production;
- PIN из `Физ!C:C` книги «Мотобелки» не мигрирует в систему.

## Карточные данные

Основная БД хранит внутренний `card_id`, `bank_id`, `last4`, статус и при необходимости ссылку на внешний защищённый token/vault. Решение о хранении иных данных требует threat model и ответа SEC-001.
