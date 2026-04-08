# Olympiad Standings

Веб-приложение для мониторинга решения олимпиадных задач школьниками.

Ключевая идея архитектуры:
- `cmd/generate` — офлайн генерация всех standings в `generated/`;
- `cmd/server` — только чтение готовых файлов и отдача HTML/API, без обращений к сайтам.

## Запуск

Требуется Go 1.22+.

### 1. Генерация данных

```bash
go run ./cmd/generate
```

Расширенный запуск:

```bash
go run ./cmd/generate -data ./data -out ./generated -group group_10a -parallel 8 -cache-ttl 5m -informatics-creds ./data/sites/informatics_credentials.json -informatics-state ./generated/cache/informatics_runs_state.json
```

Примечание:
- `-informatics-state` хранит persisted state по `run_id` для инкрементальной синхронизации Informatics между запусками;
- по умолчанию путь: `<out>/cache/informatics_runs_state.json`.

### 2. HTTP сервер

```bash
go run ./cmd/server
```

Расширенный запуск:

```bash
go run ./cmd/server -addr :8080 -generated ./generated -templates ./web/templates -static ./web/static
```

После запуска:
- `http://localhost:8080/standings` — пока пустая страница (без таблиц);
- `http://localhost:8080/standings/group_10a` — standings конкретной группы;
- `http://localhost:8080/` — `404 Not Found`.

## Olympiad-режим контестов

В `data/contests.json` у каждого контеста есть флаг:

```json
{
  "id": "contest_cf_live",
  "title": "Codeforces (реальные данные)",
  "olympiad": true,
  "subcontests": []
}
```

Поведение:
- `olympiad: false` — классический режим: `+`, `×`, пусто;
- `olympiad: true` — в ячейке задачи показывается балл:
  - для `codeforces` и `informatics` берётся реальный score;
  - для сайтов без score-поддержки (например, `acmp`) используется fallback: `1` для solved, `0` для attempted-only;
  - при отсутствии попыток ячейка пустая.

Сортировка строк в контесте:
- `olympiad: false` — по `solved_count desc`, затем по ФИО;
- `olympiad: true` — по `total_score desc`, затем `solved_count desc`, затем по ФИО.

## Форматы generated

Генератор пишет:
- `generated/groups.json` — список групп;
- `generated/standings/{group}.json` — готовые standings групп.

Для `olympiad: true` в `generated/standings/{group}.json` дополнительно заполняются:
- `contest.olympiad`;
- `row.total_score`;
- `row.scores` (массив `int|null` в порядке задач контеста).

## API

- `GET /healthz`
- `GET /api/groups`
- `GET /api/groups/{group_name}/standings`

## Как работает генерация

Pipeline генератора:
1. Берутся только группы с `update: true` (с учётом `-group`, если он задан).
2. По задачам этих групп определяется, какие сайты реально нужны для каждой группы.
3. Для учеников из этих групп строятся пары `(student, site)` и запрашиваются только нужные аккаунты нужных сайтов.
4. Генерируются и перезаписываются только `generated/standings/{group}.json` для `update: true` групп.

Важно:
- группы с `update: false` не пересчитываются и их `generated/standings/{group}.json` не трогаются;
- если поле `update` не указано, используется значение по умолчанию `true`.

## Интеграции сайтов

Реализованы клиенты:
- `internal/sites/informatics.go`
- `internal/sites/codeforces.go`
- `internal/sites/acmp.go`

Примечания:
- `informatics` использует вход через Moodle (`/login/index.php`) и получает посылки через `/py/problem/0/filter-runs`; реализована инкрементальная синхронизация по `run_id` между запусками;
- `codeforces` использует API `user.status`;
- `acmp` парсит страницу пользователя `index.asp?main=user&id=...`.

## Как добавить новый сайт

1. Добавить реализацию `SiteClient` в `internal/sites/<site>.go`.
2. Реализовать методы интерфейса:
   - `FetchUserResults(ctx, accountID) ([]TaskResult, error)`;
   - `MatchTaskURL(taskURL string) bool`;
   - `SupportsTaskScores() bool`.
3. В `FetchUserResults` возвращать:
   - `TaskURL` (сырой URL задачи на сайте),
   - `Attempted`,
   - `Solved`,
   - `Score` (`nil`, если сайт не отдаёт score).
4. Убедиться, что URL задач стабильно нормализуются (`NormalizeTaskURL`) и совпадают с URL в `data/contests.json`.
5. Зарегистрировать клиент в `cmd/generate/main.go`.
6. Добавить аккаунты сайта в `data/students.json` (`site` + `account_id`).
7. Если интеграции нужен логин/токен, добавить загрузку конфига/секретов в `cmd/generate/main.go` (по аналогии с `-informatics-creds`).
8. Перезапустить `go run ./cmd/generate`.
