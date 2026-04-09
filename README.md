# Olympiad Standings

Веб-приложение для мониторинга решения олимпиадных задач школьниками.

Ключевая идея архитектуры:
- `cmd/generate` — офлайн генерация всех standings в `generated/`;
- `cmd/server` — чтение готовых файлов и отдача HTML/API, плюс intake endpoint для сбора анкет в отдельный JSON.

## Запуск

Требуется Go 1.22+.

### 1. Генерация данных

```bash
go run ./cmd/generate
```

При первом запуске генератор автоматически создаёт пустые:
- `data/students.json`
- `data/contests.json`
- `data/groups/group_example/contests.json`
- `data/groups/group_example/groups.json`

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
go run ./cmd/server -addr :8080 -generated ./generated -data ./data -intake ./data/student_intake.json -templates ./web/templates -static ./web/static
```

После запуска:
- `http://localhost:8080/standings` — пока пустая страница (без таблиц);
- `http://localhost:8080/standings/group_10a` — standings конкретной группы;
- `http://localhost:8080/standings/group_10a/summary` — сводная таблица по всем контестам группы;
- `http://localhost:8080/standings/group_10a/summary-edu` — сводная таблица по всем task-based контестам группы;
- `http://localhost:8080/standings/group_10a/summary-olymp` — сводная таблица по всем provider-контестам группы;
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

Для provider-контестов дополнительно могут заполняться:
- `row.place`;
- `row.penalty`;
- `row.provider_status` (необязательный произвольный статус: изменение рейтинга, диплом и т.п.).

## API

- `GET /healthz`
- `GET /api/groups`
- `GET /api/groups/{group_name}/standings`
- `POST /api/rpc` (`student_intake.submit`)

## Student Intake

Сервер принимает JSON-RPC запросы на `POST /api/rpc`.

Поддерживаемый метод:
- `student_intake.submit`

Параметры:
- `full_name` (обязательный);
- любые другие строковые поля считаются аккаунтами сайтов (`site = имя_поля`, `account_id = значение`).

Правила записи в `data/student_intake.json`:
- файл хранится в формате массива студентов (как `students.json`);
- значения trim-ятся, пустые поля не сохраняются;
- при повторной анкете того же `full_name` запись обновляется;
- `id` генерируется из `full_name` (`Иванов Иван Петрович` -> `ivanov-ip`);
- при коллизии `id` добавляется суффикс `-2`, `-3`, ...

Пример JSON-RPC запроса:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "student_intake.submit",
  "params": {
    "full_name": "Иванов Иван Иванович",
    "codeforces": "tourist",
    "informatics": "12345",
    "acmp": "777"
  }
}
```

## Merge Intake -> Students

Отдельная команда для вливания анкет:

```bash
go run ./cmd/merge_students -data ./data -students ./data/students.json -intake ./data/student_intake.json -dry-run
```

Запись в `students.json`:

```bash
go run ./cmd/merge_students -data ./data -students ./data/students.json -intake ./data/student_intake.json -write
```

Поведение merge:
- сначала поиск существующего ученика по точному `full_name`, затем по `id`;
- при обновлении существующего ученика его текущий `id` сохраняется;
- непустые поля intake обновляют/добавляют данные, отсутствующие поля ничего не удаляют;
- аккаунты merge-ятся по `site`: обновление существующих или добавление новых;
- новые ученики добавляются в конец массива;
- после успешного запуска с `-write` файл intake очищается до `[]`.

Пример до/после:

`students.json` (до):

```json
[
  {
    "id": "admin",
    "full_name": "Иванов Иван Иванович",
    "accounts": [
      {"site": "codeforces", "account_id": "old_cf"}
    ]
  }
]
```

`student_intake.json`:

```json
[
  {
    "id": "ivanov-ii",
    "full_name": "Иванов Иван Иванович",
    "accounts": [
      {"site": "codeforces", "account_id": "new_cf"},
      {"site": "acmp", "account_id": "777"}
    ]
  },
  {
    "id": "petrov-pp",
    "full_name": "Петров Петр Петрович",
    "accounts": [
      {"site": "informatics", "account_id": "12345"}
    ]
  }
]
```

`students.json` (после merge):

```json
[
  {
    "id": "admin",
    "full_name": "Иванов Иван Иванович",
    "accounts": [
      {"site": "codeforces", "account_id": "new_cf"},
      {"site": "acmp", "account_id": "777"}
    ]
  },
  {
    "id": "petrov-pp",
    "full_name": "Петров Петр Петрович",
    "accounts": [
      {"site": "informatics", "account_id": "12345"}
    ]
  }
]
```

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
  - `status` / `статус`,
  - `skip` / `пропустить`;
- `auto_find` (`true/false`) — если `true`, учитываются только строки, где колонка `name` содержит:
  - префикс `full_name` ученика (из `students.json`), или
  - префикс `public_name`, или
  - префикс `public_name` без последнего пробела между инициалами (например, `И. О.` -> `И.О.`);
- `search_prefixes` (опционально) — дополнительные префиксы для фильтрации при `auto_find=true`.
  - если строка найдена по `search_prefixes`, она добавляется в вывод отдельной строкой с исходным именем из таблицы (без замены на `public_name`).

Логика `auto_find`:
- все префиксы собираются в бор (trie);
- для каждой строки берётся нормализованное имя и посимвольно сравнивается с бором;
- если перехода в боре нет, строка сразу отбрасывается;
- если достигнут терминальный узел префикса, строка принимается.

Логика выбора таблиц:
- провайдер просматривает все `<table>` на странице;
- выбирает первую таблицу, где есть строки с числом столбцов `len(columns)`;
- если первая подходящая таблица не найдена, провайдер возвращает ошибку.
