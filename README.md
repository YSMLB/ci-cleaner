# ci-cleaner

Утилита для Linux освобождает место на VPS с CI-раннерами. За один запуск она проверяет свободное место на разделе с кэшем указанного Buildx-билдера. По умолчанию работает в dry-run.

| Условие | Действие |
|---|---|
| Свободно не меньше порога | Проверяет доступность билдера через `buildx inspect` и завершает проход |
| Свободно меньше порога, dry-run | Показывает сводку `buildx du` и аргументы запланированной очистки |
| Свободно меньше порога, включён `--apply` | Выполняет `buildx prune` с фильтром по времени последнего использования кэша и повторно измеряет свободное место |

## Сборка и первый запуск

На VPS нужны Linux, Docker CLI с плагином Buildx и доступ к Docker от имени пользователя, запускающего утилиту. Для таймера нужен systemd. Из корня репозитория с Go 1.21 или новее выполните:

```sh
go test ./...
go build -o ci-cleaner .
sudo install -m 0755 ci-cleaner /usr/local/bin/ci-cleaner
sudo docker buildx ls
sudo docker info --format '{{.DockerRootDir}}'
```

Выберите локальный выделенный билдер, который используют CI-сборки. Сервис запускается от root, поэтому билдер должен быть виден в выводе **sudo docker buildx ls**. Укажите каталог на разделе, где физически хранится его кэш. Для стандартной установки с локальным Docker volume это обычно раздел DockerRootDir. Удалённый билдер хранит кэш на другом хосте.

```sh
ci-cleaner --help
sudo ci-cleaner --docker-dir /var/lib/docker --builder ci-builder
# Реальная очистка при нехватке места
sudo ci-cleaner --docker-dir /var/lib/docker --builder ci-builder --apply
```

| Флаг | Значение |
|---|---|
| `--docker-dir` | Обязательный существующий каталог на разделе билдера |
| `--builder` | Обязательное имя CI-билдера |
| `--min-free-gib` | Порог в ГиБ, по умолчанию 25. Значение 0 отключает порог |
| `--min-cache-age` | Минимальный возраст с последнего использования записи кэша, по умолчанию 30m |
| `--apply` | Разрешить удаление подходящего кэша |

Используйте один каталог для всех запусков на VPS. Утилита удерживает блокировку на нём до конца прохода. Параллельный запуск с тем же каталогом завершится с результатом `already_running`. Общий таймаут для команд Docker — две минуты.

## Таймер на VPS

Установите файлы из репозитория:

```sh
sudo install -m 0644 deploy/systemd/ci-cleaner.service deploy/systemd/ci-cleaner.timer /etc/systemd/system/
sudoedit /etc/default/ci-cleaner
```

Пример содержимого для вашего VPS:

```ini
DOCKER_DIR=/var/lib/docker
BUILDER=ci-builder
MIN_FREE_GIB=25
MIN_CACHE_AGE=30m
APPLY_FLAG=
```

Сначала проверьте один проход и журнал, затем включите таймер:

```sh
sudo systemctl daemon-reload
sudo systemctl start ci-cleaner.service
sudo journalctl -u ci-cleaner.service -n 50 --no-pager
sudo systemctl enable --now ci-cleaner.timer
systemctl list-timers ci-cleaner.timer
```

Первый запуск запланирован через две минуты после загрузки, последующие — через десять минут после завершения прохода. Чтобы включить очистку, задайте `APPLY_FLAG=--apply` в файле настроек. Сервис прочитает его при следующем запуске. Для возврата к dry-run очистите значение. Чтобы отключить расписание, выполните `sudo systemctl disable --now ci-cleaner.timer`.

В журнале `free_before_kib` и `free_after_kib` указаны в КиБ. После prune свободное место может остаться ниже порога, если подходящего кэша мало. Код выхода 0 означает успешный проход, пропуск или вывод справки, 1 — ошибку выполнения, 2 — ошибку аргументов.

Для очистки по таймеру при любом объёме свободного места задайте `MIN_FREE_GIB=0` и `APPLY_FLAG=--apply` в `/etc/default/ci-cleaner`. Фильтр `MIN_CACHE_AGE` применяется при каждом запуске.
