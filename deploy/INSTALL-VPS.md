# Обновление CI VPS

Этот комплект рассчитан на существующую службу `ci-cleaner.service` на VPS с Linux x86_64.

## Обновить cleaner

Загрузите архив на VPS. Выполните под root из нового пустого каталога:

```sh
python3 -m zipfile -e /root/ci-cleaner-vps-one-pass-linux-amd64.zip .
chmod 0755 ./ci-cleaner
flock -w 30 /run/krokodai-ci-cache-gc.lock ./ci-cleaner --docker-dir /var/lib/docker --builder default --min-free-gib 0 --prune-ci-resources
# После проверки кандидатов
bash deploy/install-vps.sh
systemctl list-timers --all ci-cleaner.timer
```

Установщик сохранит бинарник, настройки и старый таймер в каталоге `/root/ci-cleaner-backup.*` и напечатает его путь. Он сохранит раздел и билдер из `/etc/default/ci-cleaner`. Таймер вызывает `ci-cleaner-watch.service` каждую минуту. При остатке от 15 ГиБ очистка запускается раз в 30 минут. При переходе ниже 15 ГиБ она запускается на первом низком замере и повторяется раз в 5 минут, пока запас не восстановится.

После очистки cleaner проверяет отдельный запас. Начальные значения — 15 ГиБ и 10 000 inode. Для другого запаса после очистки можно указать, например, `bash deploy/install-vps.sh 20 20000`. Порог переключения расписания задаётся в скрипте проверки.

```sh
journalctl -u ci-cleaner-watch.service -u ci-cleaner.service -n 40 --no-pager
```

В свежей записи при реальной очистке ищите `mode=apply`, `threshold_gib=0` и `min_free_after_gib=15` или свой минимум. При нехватке места после очистки служба выдаёт `result=low_space_after_prune` и код 1. Для уведомления команды подключите событие к действующему мониторингу.

## Что попадает в один проход

Установщик включает `CI_CLEANER_PRUNE_RESOURCES=true`, `MIN_CACHE_AGE=30m` и `--apply` для следующего планового запуска.

- Строки `candidate=image` перечисляют образы, созданные минимум 30 минут назад. Клинер распознаёт репозитории сборок `issue95-dependency-test`, `issue156migrate…`, `i95cmp…`, `bot-restart-brc…` и метку `ci-cleaner.gc=true`. Для образов без тегов также учитывает метку проекта Compose тестового запуска. Проверяет ссылки всех контейнеров, включая остановленные, и сохраняет образы со сторонними тегами.
- Строки `candidate=volume` перечисляют именованные и анонимные Docker-тома с известным временем создания, которым исполнилось 30 минут и которыми сейчас не пользуется ни один контейнер. Том, отсоединённый совсем недавно, тоже подходит, если с создания прошло 30 минут. Содержимое тома удаляется целиком. Клинер повторно сверяет имя, драйвер и время создания, затем вызывает Docker без принудительного удаления.
- В конце прохода BuildKit очищает доступный для удаления кэш с фильтром по последнему использованию `until=30m`. В dry-run команда `buildx du` показывает инвентарь билдера. Итоговый объём освобождённого места виден в журнале после реального прохода.

При большом количестве томов служба может работать до 21 минуты. Блокировка защищает от наложения проходов. Чтобы оставить только уборку кэша, задайте `CI_CLEANER_PRUNE_RESOURCES=false` в `/etc/default/ci-cleaner`.

## Подключить проверку раннеров

Установщик уже копирует `ci-runner-preflight`, `ci-build-guard` и создаёт общий lock. Посмотрите имена служб:

```sh
systemctl list-units --all --type=service 'actions.runner.*'
```

В `.env` рядом с `runsvc.sh` **каждого раннера** добавьте настройки ниже, указав реальный путь к его существующему `_work`. Если в `.env` уже задан `ACTIONS_RUNNER_HOOK_JOB_STARTED`, добавьте вызов `/usr/local/bin/ci-runner-preflight` в существующий хук.

```ini
ACTIONS_RUNNER_HOOK_JOB_STARTED=/usr/local/bin/ci-runner-preflight
CI_RUNNER_DOCKER_DIR=/var/lib/docker
CI_RUNNER_WORK_DIR=/actual/path/to/runner/_work
CI_RUNNER_MIN_FREE_GIB=15
CI_RUNNER_MIN_FREE_INODES=10000
```

При первом подключении хука проверьте пути и доступ от имени пользователя службы с помощью `ci-cleaner check-space --min-free-gib 15 --min-free-inodes 10000 /var/lib/docker /actual/path/to/runner/_work`. Перезапустите службы раннеров по одной после завершения активных заданий. При обновлении уже подключённого скрипта его прочитает следующее задание. При нехватке места или inode хук пишет предупреждение, и задание продолжается. Ошибка самого замера по-прежнему завершает хук с ошибкой.

В workflow оберните тяжёлую команду целиком:

```sh
ci-build-guard --min-free-gib 25 --min-free-inodes 10000 \
  /var/lib/docker "$GITHUB_WORKSPACE" -- docker build -t app:test .
```

Сборки под общей блокировкой проверяют место по очереди. Выберите минимум по измеренному пику потребления. Для защиты системного раздела от уже запущенной сборки отдельно настройте изоляцию данных Docker и CI на уровне разделов или квот.

## Откат

Если возвращается версия без `check-space`, сначала снимите хук и вызовы обёртки, дождитесь завершения активных заданий и перезапустите службы раннеров. Подставьте каталог, напечатанный установщиком, и выполните под root:

```sh
backup=/root/ci-cleaner-backup.XXXXXXXX
systemctl stop ci-cleaner.timer &&
install -m 0755 "$backup/ci-cleaner" /usr/local/bin/ci-cleaner &&
cp -a "$backup/default-ci-cleaner" /etc/default/ci-cleaner &&
cp -a "$backup/ci-cleaner.timer" /etc/systemd/system/ci-cleaner.timer &&
{ if [ -f "$backup/resources-timeout.conf" ]; then cp -a "$backup/resources-timeout.conf" /etc/systemd/system/ci-cleaner.service.d/; else rm -f /etc/systemd/system/ci-cleaner.service.d/resources-timeout.conf; fi; } &&
systemctl daemon-reload &&
systemctl restart ci-cleaner.timer
```
