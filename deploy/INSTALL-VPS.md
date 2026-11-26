# Обновление CI VPS

Этот комплект рассчитан на существующую службу `ci-cleaner.service` на VPS с Linux x86_64.

## Обновить cleaner

Загрузите архив на VPS. Выполните под root из нового пустого каталога:

```sh
python3 -m zipfile -e /root/ci-cleaner-vps-resources-linux-amd64.zip .
bash deploy/install-vps.sh
systemctl list-timers --all ci-cleaner.timer
```

Установщик сохранит бинарник, настройки и старый таймер в каталоге `/root/ci-cleaner-backup.*` и напечатает его путь. Он сохранит раздел, билдер и фильтр возраста из `/etc/default/ci-cleaner`. Новый таймер вызывает `ci-cleaner-watch.service` каждую минуту. При остатке от 15 ГиБ очистка запускается раз в 30 минут. При переходе ниже 15 ГиБ она запускается на первом низком замере и повторяется раз в 5 минут, пока запас не восстановится.

После очистки cleaner проверяет отдельный запас. Начальные значения — 15 ГиБ и 10 000 inode. Для другого запаса после очистки можно указать, например, `bash deploy/install-vps.sh 20 20000`. Порог переключения расписания задаётся в скрипте проверки.

```sh
journalctl -u ci-cleaner-watch.service -u ci-cleaner.service -n 40 --no-pager
```

В свежей записи при реальной очистке ищите `mode=apply`, `threshold_gib=0` и `min_free_after_gib=15` или свой минимум. При нехватке места после очистки служба выдаёт `result=low_space_after_prune` и код 1. Для уведомления команды подключите событие к действующему мониторингу.

## Включить уборку образов и томов

После установки проверьте отбор под действующей блокировкой, без `--apply`. Подставьте параметры своего Docker из файла настроек:

```sh
flock -n -E 75 /run/krokodai-ci-cache-gc.lock \
  /usr/local/bin/ci-cleaner --docker-dir /var/lib/docker --builder default \
  --min-free-gib 0 --prune-ci-resources
```

Образы появляются в строках `candidate=image`. Утилита включает временные образы из workflow `issue95-dependency-test` и `issue156migrate`, а также образы с меткой `ci-cleaner.gc=true`. Они должны быть созданы не менее 30 минут назад и не использоваться контейнерами, включая остановленные. Образы с несколькими тегами сохраняются.

После проверки добавьте `CI_CLEANER_PRUNE_RESOURCES=true` в `/etc/default/ci-cleaner`. На первом проходе cleaner запишет список непривязанных локальных томов в `/var/lib/ci-cleaner/unattached-volumes.json`. После 30 минут том может быть удалён, если он снова виден как непривязанный и совпадает время его создания. Перед удалением cleaner перепроверяет это время и вызывает Docker без `--force`. Том с настройками внешнего или bind-монтирования пропускается. Для возврата к уборке кэша установите `CI_CLEANER_PRUNE_RESOURCES=false`.

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
systemctl daemon-reload &&
systemctl restart ci-cleaner.timer
```
