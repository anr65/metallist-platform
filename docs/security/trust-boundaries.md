# Границы доверия

Статус: проект  
Дата: 2026-09-11

| Граница | Данные | Основные риски | Требуемый контроль |
|---|---|---|---|
| Telegram → bot | update, user ID, команда | подмена пользователя, replay, неверный parse | binding identity, update idempotency, strict parser |
| XLSX → API | файл и строки | formula/macro payload, oversized file, дубли, неверные типы | size/type limits, no execution, versioned mapping, preview |
| Browser → API | команды и отчёты | session theft, broken authorization, mass assignment | secure session, RBAC, validation, audit |
| API → PostgreSQL | бизнес-события и postings | SQL injection, partial commit, privilege excess | parameters, transaction, least privilege, constraints |
| API → S3 | originals и attachments | public access, overwrite, exfiltration | private bucket, checksum, versioning, signed URL |
| CI → server | image/version | supply-chain compromise, secret leak | protected branch, immutable artifact, scoped identity |
| operator → cash | физические деньги | недостача, неподтверждённая передача | custody event, confirmation, reconciliation |
| observation → ledger | фактический остаток | перезапись расчётной истории | separate observation, explicit resolution event |

До production выполняется repository-specific threat model после появления кода и deployment configuration.
