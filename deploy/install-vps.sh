#!/bin/bash
set -euo pipefail

if [[ ${1:-} == --help ]]; then
    echo 'Usage: bash deploy/install-vps.sh [minimum-free-GiB-after-prune] [minimum-free-inodes-after-prune]'
    exit 0
fi

reserve_gib=${1:-15}
reserve_inodes=${2:-10000}
if (( $# > 2 )) || [[ ! $reserve_gib =~ ^[1-9][0-9]*$ || ! $reserve_inodes =~ ^[1-9][0-9]*$ ]]; then
    echo 'The installer requires positive integer reserves.' >&2
    exit 2
fi
if (( EUID != 0 )) || [[ $(uname -m) != x86_64 ]]; then
    echo 'Run as root on the x86_64 VPS.' >&2
    exit 2
fi

bundle=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
config=/etc/default/ci-cleaner
binary=/usr/local/bin/ci-cleaner
timer=/etc/systemd/system/ci-cleaner.timer
timeout_override=/etc/systemd/system/ci-cleaner.service.d/resources-timeout.conf
for path in "$config" "$binary" "$timer" "$bundle/ci-cleaner" \
    "$bundle"/deploy/runner/{ci-build-guard,ci-build-guard.conf,ci-runner-preflight} \
    "$bundle"/deploy/systemd/{ci-cleaner-watch.sh,ci-cleaner-watch.service,ci-cleaner.timer,resources-timeout.conf}; do
    if [[ ! -f $path ]]; then
        echo "Required file missing: $path" >&2
        exit 2
    fi
done
command -v flock systemd-tmpfiles systemctl >/dev/null
sh -n "$bundle/deploy/systemd/ci-cleaner-watch.sh"

exec 9<>/run/krokodai-ci-cache-gc.lock
if ! flock -w 30 9; then
    echo 'The cleaner lock is busy. Retry when the current pass ends.' >&2
    exit 75
fi

backup=$(mktemp -d /root/ci-cleaner-backup.XXXXXXXX)
cp -a -- "$config" "$backup/default-ci-cleaner"
cp -a -- "$binary" "$backup/ci-cleaner"
cp -a -- "$timer" "$backup/ci-cleaner.timer"
if [[ -f $timeout_override ]]; then
    cp -a -- "$timeout_override" "$backup/resources-timeout.conf"
fi
echo "Backup: $backup"

new_config=$(mktemp "${config}.XXXXXX")
new_binary=$(mktemp "${binary}.XXXXXX")
trap 'rm -f -- "$new_config" "$new_binary"' EXIT
awk '!/^[[:space:]]*(MIN_FREE_GIB|MIN_CACHE_AGE|APPLY_FLAG|CI_CLEANER_PRUNE_RESOURCES|CI_CLEANER_MIN_FREE_AFTER_GIB|CI_CLEANER_MIN_FREE_AFTER_INODES)[[:space:]]*=/' \
    "$config" > "$new_config"
printf 'MIN_FREE_GIB=0\nMIN_CACHE_AGE=30m\nAPPLY_FLAG=--apply\nCI_CLEANER_PRUNE_RESOURCES=true\nCI_CLEANER_MIN_FREE_AFTER_GIB=%s\nCI_CLEANER_MIN_FREE_AFTER_INODES=%s\n' \
    "$reserve_gib" "$reserve_inodes" >> "$new_config"
chmod --reference="$config" "$new_config"

install -m 0755 "$bundle/ci-cleaner" "$new_binary"
"$new_binary" check-space --help >/dev/null 2>&1
install -m 0755 "$bundle"/deploy/runner/{ci-build-guard,ci-runner-preflight} /usr/local/bin/
install -m 0644 "$bundle/deploy/runner/ci-build-guard.conf" /etc/tmpfiles.d/
systemd-tmpfiles --create /etc/tmpfiles.d/ci-build-guard.conf
mv -fT -- "$new_binary" "$binary"
if ! mv -fT -- "$new_config" "$config"; then
    install -m 0755 "$backup/ci-cleaner" "$new_binary"
    mv -fT -- "$new_binary" "$binary"
    echo 'Settings could not be installed. The previous binary was restored.' >&2
    exit 1
fi

install -m 0755 "$bundle/deploy/systemd/ci-cleaner-watch.sh" /usr/local/bin/
install -D -m 0644 "$bundle/deploy/systemd/resources-timeout.conf" "$timeout_override"
install -m 0644 "$bundle/deploy/systemd/ci-cleaner-watch.service" \
    "$bundle/deploy/systemd/ci-cleaner.timer" /etc/systemd/system/

systemctl daemon-reload
systemctl restart ci-cleaner.timer

echo 'Cleaner updated. Timer restarted with minute-by-minute checks.'
echo 'Runner tools installed. See deploy/INSTALL-VPS.md to enable the hook and guard.'
