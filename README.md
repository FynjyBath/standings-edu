# Olympiad Standings MVP (Go)

MVP веб-приложения для мониторинга решений олимпиадных задач школьниками по группам.

Ключевой принцип архитектуры:
- тяжёлая агрегация выполняется отдельной админ-командой `generate`;
- публичный сервер `server` только читает уже готовые файлы из `generated/`.

## Архитектура

Проект разделён на 2 независимых процесса:

1. `cmd/generate`
- читает исходные JSON из `data/`;
- через site adapters получает статусы задач по аккаунтам;
- нормализует URL и агрегирует статусы по школьникам;
- строит standings по группам;
- сохраняет результат в `generated/groups.json` и `generated/standings/{group}.json`.

2. `cmd/server`
- не обращается к внешним сайтам;
- не пересчитывает standings на HTTP-запросах;
- отдает API и HTML только из `generated/`.

## Структура каталогов

```text
.
├── cmd
│   ├── generate/main.go
│   └── server/main.go
├── data
│   ├── students.json
│   ├── contests.json
│   └── groups
│       ├── group_10a
│       │   ├── group.json
│       │   └── contests.json
│       └── group_10b
│           ├── group.json
│           └── contests.json
├── generated
│   ├── groups.json
│   └── standings
│       ├── group_10a.json
│       └── group_10b.json
├── internal
│   ├── cache
│   │   └── ttl_cache.go
│   ├── domain
│   │   ├── models.go
│   │   └── url.go
│   ├── generator
│   │   └── generator.go
│   ├── httpapi
│   │   ├── handlers.go
│   │   └── router.go
│   ├── service
│   │   └── standings_builder.go
│   ├── sites
│   │   ├── client.go
│   │   ├── registry.go
│   │   ├── informatics.go
│   │   └── codeforces.go
│   ├── storage
│   │   ├── source_loader.go
│   │   ├── generated_loader.go
│   │   └── generated_writer.go
│   └── web
│       └── templates.go
├── web
│   ├── static/styles.css
│   └── templates
│       ├── layout.html
│       ├── index.html
│       └── group_standings.html
└── go.mod
```

## Формат исходных данных (`data/`)

### `data/students.json`

```json
[
  {
    "id": "stu_1",
    "full_name": "Иванов Иван Иванович",
    "accounts": [
      {"site": "informatics", "account_id": "436037"},
      {"site": "codeforces", "account_id": "tourist"}
    ]
  }
]
```

### `data/contests.json`

```json
[
  {
    "id": "contest_graphs",
    "title": "Графы",
    "subcontests": [
      {
        "title": "DFS и BFS",
        "tasks": [
          "https://site1.example/problem/1",
          "https://site1.example/problem/2"
        ]
      }
    ]
  }
]
```

### `data/groups/{group_slug}/group.json`

```json
{
  "title": "Группа 10А",
  "student_ids": ["stu_1", "stu_2"]
}
```

### `data/groups/{group_slug}/contests.json`

```json
["contest_graphs", "contest_dp"]
```

## Формат generated данных

### `generated/groups.json`

```json
[
  {"slug": "group_10a", "title": "Группа 10А"}
]
```

### `generated/standings/{group_slug}.json`

Содержит:
- `group_slug`, `group_title`;
- `contests[]` в порядке группы;
- внутри контеста:
  - `id`, `title`;
  - `subcontests[]` (с `task_count` и задачами);
  - плоский `tasks[]` в порядке отображения;
  - `rows[]` (ученики, `solved_count`, `statuses[]`).

Статусы:
- `solved`
- `attempted`
- `none`

## Бизнес-правила (реализовано)

- сравнение задач только по нормализованному URL (`NormalizeTaskURL`);
- приоритет статуса: `solved > attempted > none`;
- буквы задач (`A`, `B`, `C`, ...) нумеруются заново внутри каждого подконтеста;
- сортировка строк в каждом контесте:
  1. `solved_count` по убыванию,
  2. `full_name` по возрастанию;
- неизвестные `student_id`/`contest_id` логируются и пропускаются;
- неизвестный site adapter логируется и пропускается;
- ошибка одного аккаунта/сайта не валит генерацию всей группы;
- внутри генерации есть:
  - ограничение параллелизма (goroutines + semaphore),
  - TTL cache 5 минут по ключу `(site, account_id)`,
  - dedupe in-flight запросов одного аккаунта.

## Запуск

Требуется Go 1.22+.

### 1. Админ обновляет standings

```bash
go run ./cmd/generate
```

Опциональные флаги:

```bash
go run ./cmd/generate -data ./data -out ./generated -group group_10a -parallel 8 -cache-ttl 5m
```

### 2. Публичный сервер отдаёт готовые данные

```bash
go run ./cmd/server
```

Опциональные флаги:

```bash
go run ./cmd/server -addr :8080 -generated ./generated -templates ./web/templates -static ./web/static
```

После запуска:
- `http://localhost:8080/` — список групп;
- `http://localhost:8080/standings/group_10a` — HTML standings группы.

## API

- `GET /healthz` -> `200 {"status":"ok"}`
- `GET /api/groups` -> читает `generated/groups.json`
- `GET /api/groups/{group_name}/standings` -> читает `generated/standings/{group_name}.json`

Если файла нет, возвращается `404`.

## Где добавлять реальные интеграции сайтов

Текущие stub-клиенты:
- `internal/sites/informatics.go`
- `internal/sites/codeforces.go`

Интерфейс:

```go
type SiteClient interface {
    FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error)
}
```

Чтобы добавить новый сайт:
1. Создать файл `internal/sites/<site>.go` с реализацией `SiteClient`.
2. Зарегистрировать клиент в `cmd/generate/main.go`:

```go
registry.Register("<site>", sites.New<Site>Client(...))
```

Публичный сервер при этом не меняется.

## Что сервер реально читает

Публичный сервер использует только:
- `generated/groups.json`
- `generated/standings/{group_slug}.json`

Исходные файлы `data/*.json` и site adapters используются только командой `generate`.
