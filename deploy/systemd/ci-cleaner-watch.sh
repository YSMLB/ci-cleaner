#!/bin/sh
set -eu
umask 077

state=/run/ci-cleaner-watch.state

rc=0
/usr/local/bin/ci-cleaner check-space \
    --min-free-gib 15 "${DOCKER_DIR:-/var/lib/docker}" || rc=$?

case "$rc" in
    0) mode=normal; interval=1800 ;;
    1) mode=low;    interval=300 ;;
    *) exit "$rc" ;;
esac

previous_mode=
last_attempt=
if [ -f "$state" ]; then
    read -r previous_mode last_attempt < "$state"
fi

# Секунды с загрузки Linux
read -r uptime _ < /proc/uptime
now=${uptime%%.*}

start_cleanup=no
if [ -z "$last_attempt" ] ||
   [ "$((now - last_attempt))" -ge "$interval" ] ||
   { [ "$mode" = low ] && [ "$previous_mode" != low ]; }; then
    start_cleanup=yes
    last_attempt=$now
fi

# Сохраняем режим
tmp=$(mktemp "${state}.XXXXXX")
trap 'rm -f "$tmp"' EXIT
printf '%s %s\n' "$mode" "$last_attempt" > "$tmp"
mv -f "$tmp" "$state"

if [ "$start_cleanup" = yes ]; then
    exec systemctl start ci-cleaner.service
fi