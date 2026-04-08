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

## Типы контестов

В `data/contests.json` поддерживаются два типа:
- `contest_type: "tasks"` (или отсутствие поля `contest_type`) — классический режим, контест задаётся списком задач;
- `contest_type: "provider"` — standings берутся напрямую из provider-а.

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
2. Внутри таких групп обновляются только контесты с `update: true` в `data/groups/<group>/contests.json`.
3. Для task-based контестов по задачам определяется, какие сайты реально нужны.
4. Для task-based контестов строятся пары `(student, site)` и запрашиваются только нужные аккаунты нужных сайтов.
5. Генерируются только обновляемые контесты и сливаются с предыдущими данными группы:
   - `contest.update=true` — пересчитывается;
   - `contest.update=false` — сохраняется из предыдущего `generated/standings/{group}.json` без изменений.

Важно:
- группы с `update: false` не пересчитываются и их `generated/standings/{group}.json` не трогаются;
- контесты с `update: false` не пересчитываются;
- если поле `update` не указано, используется значение по умолчанию `true`.

## Интеграции сайтов

Реализованы клиенты:
- `internal/tasks_based/informatics.go`
- `internal/tasks_based/codeforces.go`
- `internal/tasks_based/acmp.go`

Примечания:
- `informatics` использует вход через Moodle (`/login/index.php`) и получает посылки через `/py/problem/0/filter-runs`; реализована инкрементальная синхронизация по `run_id` между запусками;
- `codeforces` использует API `user.status` (task-based) и `contest.standings` (provider-based);
- `acmp` парсит страницу пользователя `index.asp?main=user&id=...`.

## Как добавить новый сайт

1. Добавить реализацию `SiteClient` в `internal/tasks_based/<site>.go`.
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
6. Добавить участника в `data/students.json`: обязательный `full_name`, опциональный `public_name` (если пустой, берётся из `full_name`), и аккаунты (`site` + `account_id`).
7. Если интеграции нужен логин/токен, добавить загрузку конфига/секретов в `cmd/generate/main.go` (по аналогии с `-informatics-creds`).
8. Перезапустить `go run ./cmd/generate`.

## Как добавить новый standings provider

1. Добавить реализацию `ContestStandingsProvider` в `internal/provider_based/<provider>.go`.
2. Реализовать методы:
   - `ProviderID() string`;
   - `BuildStandings(ctx, input) (domain.GeneratedContestStandings, error)`.
3. В `BuildStandings` вернуть `GeneratedContestStandings` в текущем формате, чтобы не менять `server/web`.
4. Зарегистрировать provider в `cmd/generate/main.go` через `ContestProviderRegistry`.
5. Создать контест в `data/contests.json` с:
   - `contest_type: "provider"`,
   - `provider: "<provider_id>"`,
   - `provider_config: {...}`.
6. Привязать контест к группе в `data/groups/<group>/contests.json`.

## Provider `html_table_import`

Провайдер для импорта standings из HTML-страницы с таблицами.

`provider_config`:
- `page_url` (обязательный) — URL страницы, где находятся таблицы;
- `columns` (обязательный) — список назначений столбцов таблицы по порядку:
  - `place` / `место`,
  - `name` / `имя`,
  - `task` / `задача` (может повторяться),
  - `penalty` / `штраф`,
  - `skip` / `пропустить`;
- `auto_find` (`true/false`) — если `true`, учитываются только строки, где колонка `name` содержит:
  - подстроку `full_name` ученика (из `students.json`), или
  - подстроку с инициалами вида `Фамилия И. О.`;
- `search_substrings` (опционально) — дополнительные подстроки для фильтрации при `auto_find=true`.

Логика выбора таблиц:
- провайдер просматривает все `<table>` на странице;
- выбирает первую таблицу, где есть строки с числом столбцов `len(columns)`;
- если первая подходящая таблица не найдена, провайдер возвращает ошибку.
