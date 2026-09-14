#!/usr/bin/env bash
set -euo pipefail

if [[ $(id -u) != 0 || ! -t 0 ]]; then
  echo 'Запустите от root в интерактивном терминале.' >&2
  exit 1
fi

set -a
. /etc/metallist-platform/demo-owner.env
set +a
identity=$(psql "$DATABASE_URL" -Atqc 'SELECT current_database()')
if [[ "$APP_ENV" != demo || "$identity" != metallist_demo ]]; then
  echo 'Ожидалась отдельная демонстрационная база metallist_demo.' >&2
  exit 1
fi

read -r -s -p 'Новый пароль главного администратора (от 16 символов): ' password
echo
read -r -s -p 'Повторите пароль: ' repeated
echo
if [[ ${#password} -lt 16 || "$password" != "$repeated" ]]; then
  echo 'Пароли не совпадают или слишком короткие.' >&2
  exit 1
fi

umask 077
password_file=$(mktemp /run/metallist-password.XXXXXXXX)
trap 'rm -f "$password_file"' EXIT
printf '%s' "$password" > "$password_file"
unset password repeated
/opt/metallist-platform/current/platform set-password chief "$password_file"
echo 'Пароль установлен. Предыдущие сеансы завершены. Логин: chief.'
