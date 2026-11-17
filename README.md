# buildyard

Личный менеджер сборок Maven-проектов: локальный демон с веб-интерфейсом. Техзадание — [docs/PLAN.md](docs/PLAN.md), итоги спайка — [docs/SPIKE.md](docs/SPIKE.md).

## Сборка и запуск

Нужен Go 1.26+.

```sh
make build          # bin/buildyard (CGO не нужен)
make test
./bin/buildyard serve   # слушает 127.0.0.1:7777, открыть http://127.0.0.1:7777/
./bin/buildyard open    # открыть UI в браузере
```

Данные лежат в `~/.buildyard` (`BUILDYARD_HOME` переопределяет): `config.toml`, `buildyard.db`, `daemon.log`.

## Статус

Фаза 1 (каркас): конфиг, lock-файл, SQLite с миграциями, CRUD проектов и экран проектов.
