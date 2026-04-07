# Olympiad Standings MVP (Go)

Веб-приложение для создания мониторов решения задач по олимпиадному программированию по группам с различных сайтов.

## Запуск

Требуется Go 1.22+.

### 0. Данные участников и групп

В `data/students.json` содержится описание логинов/id каждого участника. Каждому участнику присваивается внутренний id.

В `data/contests.json` содержится описание контестов. Каждому контесту присваивается внутренний id.

В `data/groups/...` для каждой группы создаётся отдельная папка, в которой лежат списки участников и контестов этой группы.

### 1. Скрипт генерации табличек

```bash
go run ./cmd/generate
```

Для Informatics нужен файл с логином/паролем (`data/sites/informatics_credentials.json`).

```bash
go run ./cmd/generate -data ./data -out ./generated -group group_10a -parallel 8 -cache-ttl 5m -informatics-creds ./data/sites/informatics_credentials.json
```

### 2. Сервер

```bash
go run ./cmd/server
```

```bash
go run ./cmd/server -addr :8080 -generated ./generated -templates ./web/templates -static ./web/static
```

После запуска:
- `http://localhost:8080/` — список групп;
- `http://localhost:8080/standings/group_10a` — HTML standings группы.

## Уже реализованные интеграции

Текущие клиенты:
- `internal/sites/informatics.go`
- `internal/sites/codeforces.go`
- `internal/sites/acmp.go`

Примечание:
- `informatics` использует логин в Moodle (`/login/index.php`) и получает все посылки через backend endpoint `/py/problem/0/filter-runs`, обходя UI-пагинацию;
- `codeforces` использует реальный API `user.status` по `https://codeforces.com/apiHelp` (логин не требуется).
- `acmp` получает решенные и нерешенные задачи со страницы пользователя `index.asp?main=user&id=...` (логин не требуется).

## Как добавить интеграцию сайта?

Чтобы добавить новый сайт:
1. Создать файл `internal/sites/<site>.go` с реализацией `SiteClient`.
2. Зарегистрировать клиент в `cmd/generate/main.go`.
