# Olympiad Standings

Простой сервис для ведения таблиц результатов по олимпиадным задачам.

Проект решает две задачи:
- собирает данные об учениках и их решениях из разных источников;
- показывает готовые standings в веб-интерфейсе и через API.

## Что умеет проект

- Генерирует standings в статические JSON-файлы (`generated/`);
- Поддерживает два типа контестов:
  - `tasks`: собирает решения по списку задач;
  - `provider`: берёт готовые standings у внешнего провайдера;
- Поднимает HTTP-сервер для просмотра таблиц;
- Принимает анкеты учеников (intake) через JSON-RPC;
- Вливает анкеты в основную базу учеников отдельной командой merge.

## Для обычных пользователей

### Как устроена работа проекта

Рабочий цикл выглядит так:
1. Вы храните исходные данные в `data/` (ученики, контесты, группы).
2. Запускаете генерацию (`cmd/generate`) и получаете готовые standings в `generated/`.
3. Запускаете сервер (`cmd/server`) и открываете страницы standings в браузере.
4. Новые анкеты приходят в `student_intake.json`.
5. Когда нужно, вливаете анкеты в основную базу `students.json` командой merge.

### Основные данные

Минимально важные файлы:
- `data/students.json` — ученики и их аккаунты;
- `data/contests.json` — список контестов;
- `data/groups/<group_slug>/group.json` — информация о группе;
- `data/groups/<group_slug>/contests.json` — контесты группы.

Полезные примеры лежат в `data_example/`.
Этот каталог используется только как шаблон: команды проекта читают данные из `data/`.

Важно:
- при первом запуске генератор автоматически создаст пустые `data/students.json` и `data/contests.json`, если их нет;
- группы нужно создать отдельно (можно по примеру из `data_example/groups/group_example/`).

Быстрый старт из примеров:

```bash
mkdir -p data/groups/group_example
cp data_example/students_example.json data/students.json
cp data_example/contests_example.json data/contests.json
cp data_example/groups/group_example/group_example.json data/groups/group_example/group.json
cp data_example/groups/group_example/contests_example.json data/groups/group_example/contests.json
cp data_example/student_intake_example.json data/student_intake.json
```

После этого можно сразу запускать генерацию и сервер.

### Как сгенерировать standings

Базовый запуск:

```bash
go run ./cmd/generate
```

Частые полезные флаги:

```bash
go run ./cmd/generate \
  -data-dir ./data \
  -generated-dir ./generated \
  -group group_example
```

Что произойдёт:
- в `generated/groups.json` появится список групп;
- в `generated/standings/<group>.json` появятся таблицы по группам.

### Как запустить сервер

Базовый запуск:

```bash
go run ./cmd/server
```

После `clean clone` эту команду можно запускать сразу, без ручного `mkdir`/`touch` для runtime-файлов.

При старте сервер делает bootstrap runtime-структуры:
- создаёт `generated/`, если каталога нет;
- создаёт `generated/standings/`, если каталога нет;
- создаёт `data/`, `data/groups/` и `data/sites/`, если их нет;
- создаёт `data/student_intake.json` со значением `[]`, если файла нет.

Важно:
- существующие файлы не перезаписываются;
- если по ожидаемому пути лежит объект другого типа (например, файл вместо директории), сервер завершится с понятной ошибкой;
- `web/templates` и `web/static` не генерируются: сервер только проверяет, что это существующие директории.

После запуска откройте:
- `http://localhost:8080/standings` — список/входная страница;
- `http://localhost:8080/standings/<group_slug>` — таблицы группы;
- `http://localhost:8080/standings/<group_slug>/summary` — сводка по всем контестам;
- `http://localhost:8080/standings/<group_slug>/summary-edu` — сводка по task-контестам;
- `http://localhost:8080/standings/<group_slug>/summary-olymp` — сводка по provider-контестам.

API:
- `GET /healthz`
- `GET /api/groups`
- `GET /api/groups/{group_name}/standings`
- `POST /api/rpc`

### Как отправлять анкеты (intake)

Сервер принимает JSON-RPC метод `student_intake.submit` через `POST /api/rpc`.

Минимальный пример:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "student_intake.submit",
  "params": {
    "full_name": "Иванов Иван Иванович",
    "codeforces": "tourist",
    "informatics": "12345",
    "group": "group_example"
  }
}
```

Что важно:
- `full_name` обязателен;
- остальные строковые поля считаются аккаунтами (`site=имя поля`, `account_id=значение`);
- анкеты сохраняются в `data/student_intake.json` (по умолчанию).

### Как влить анкеты в `students.json`

Проверка без записи:

```bash
go run ./cmd/merge_students -dry-run
```

Реальная запись:

```bash
go run ./cmd/merge_students -write
```

По умолчанию команда использует:
- `data/students.json`
- `data/student_intake.json`

После `-write`:
- `students.json` обновляется;
- `student_intake.json` очищается (`[]`).

### Типичный сценарий использования

1. Подготовить `data/students.json`, `data/contests.json` и файлы групп.
2. Запустить `go run ./cmd/generate`.
3. Запустить `go run ./cmd/server`.
4. Открыть `/standings/<group_slug>` и проверить результат.
5. Принимать новые анкеты через `POST /api/rpc`.
6. Периодически запускать `go run ./cmd/merge_students -write`.
7. Снова запускать генерацию, чтобы standings учитывали обновлённых учеников.

## Для разработчиков

### Как устроен проект

На высоком уровне:
- `cmd/generate` — офлайн pipeline генерации;
- `cmd/server` — веб и API над уже сгенерированными файлами;
- `cmd/merge_students` — merge intake в основную базу учеников.

### Основные директории и ответственность

- `cmd/` — точки входа CLI;
- `internal/domain/` — доменные модели и общие правила нормализации;
- `internal/storage/` — чтение исходников из `data/` и запись `generated/`;
- `internal/source/` — внешние интеграции:
  - task-сайты (`SiteClient`),
  - standings-провайдеры (`ContestProvider`),
  - единый `Registry`;
- `internal/standings/` — основной pipeline построения standings;
- `internal/studentintake/` — intake + merge + синхронизация группы;
- `internal/httpapi/`, `internal/web/` — HTTP-слой и шаблоны.

### Поток данных

1. `storage.SourceLoader` читает исходные JSON из `data/`.
2. `standings.Builder`:
   - выбирает нужные группы/контесты;
   - для task-контестов собирает результаты через `source.SiteClient`;
   - для provider-контестов вызывает `source.ContestProvider`.
3. `standings.Pipeline` объединяет обновлённые и неизменяемые контесты группы.
4. `storage.GeneratedWriter` пишет итог в `generated/`.
5. `httpapi` читает `generated/` и отдаёт UI/API.

### Как добавить новый сайт (task-based)

1. Создайте реализацию `SiteClient` в `internal/source/<site>.go`.
2. Реализуйте методы:
   - `FetchUserResults(ctx, accountID) ([]TaskResult, error)`
   - `MatchTaskURL(taskURL string) bool`
   - `SupportsTaskScores() bool`
3. Зарегистрируйте клиент в `cmd/generate/main.go`:
   - `registry.RegisterSite("<site>", client)`
4. Добавьте аккаунты этого сайта в `data/students.json`.

Почему это важно:
- `MatchTaskURL` влияет на определение источника для задач;
- если URL задач определяются неверно, решения не попадут в standings.

### Как добавить нового standings provider

1. Создайте реализацию `ContestProvider` в `internal/source/<provider>.go`.
2. Реализуйте:
   - `ProviderID() string`
   - `BuildStandings(ctx, input) (domain.GeneratedContestStandings, error)`
3. Зарегистрируйте в `cmd/generate/main.go`:
   - `registry.RegisterProvider(provider)`
4. Добавьте контест в `data/contests.json`:
   - `contest_type: "provider"`
   - `provider: "<provider_id>"`
   - `provider_config: {...}`
5. Подключите контест к группе через `data/groups/<group>/contests.json`.

### На что смотреть при расширении

- Нормализация URL задач:
  - используйте единый `domain.NormalizeTaskURL`;
  - в `data/contests.json` URL должны совпадать по смыслу с тем, что возвращают клиенты.
- Нормализация сайтов и аккаунтов:
  - site хранится в нижнем регистре;
  - пустые `account_id` не должны попадать в данные.
- Совместимость выходного формата:
  - `BuildStandings` провайдера должен заполнять `domain.GeneratedContestStandings` без нарушения текущих полей UI/API.

### Как проверить, что интеграция не ломает flow

Минимальный чеклист:
1. `go test ./...`
2. `go run ./cmd/generate -group <test_group>`
3. Проверить `generated/standings/<test_group>.json`
4. Запустить `go run ./cmd/server` и открыть страницу группы
5. Проверить API `GET /api/groups/{group}/standings`

## Краткий блок команд

```bash
# 1) Генерация standings
go run ./cmd/generate

# 2) Запуск сервера
go run ./cmd/server

# 3) Проверка intake merge без записи
go run ./cmd/merge_students -dry-run

# 4) Merge с записью
go run ./cmd/merge_students -write
```

## FAQ / примечания

**Почему на странице нет таблиц?**
- Обычно не запускалась генерация или для группы нет обновляемых контестов.

**Нужны ли креды Informatics всегда?**
- Нет, только если вы используете `informatics` как источник задач.

**Можно ли запускать только одну группу?**
- Да: `go run ./cmd/generate -group <group_slug>`.

**Где смотреть примеры входных данных?**
- Все примеры лежат в `data_example/` и не используются автоматически.

**Нужно ли вручную создавать `generated/` и `student_intake.json` перед `cmd/server`?**
- Нет. Сервер сам создаёт недостающие runtime-пути при старте.
