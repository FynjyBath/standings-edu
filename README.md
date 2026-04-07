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
go run ./cmd/generate -data ./data -out ./generated -group group_10a -parallel 8 -cache-ttl 5m -informatics-creds ./data/sites/informatics_credentials.json
```

### 2. HTTP сервер

```bash
go run ./cmd/server
```

Расширенный запуск:

```bash
go run ./cmd/server -addr :8080 -generated ./generated -templates ./web/templates -static ./web/static
```

После запуска:
- `http://localhost:8080/standings` — сводная таблица по всем участникам: solved по сайтам + total;
- `http://localhost:8080/standings/group_10a` — standings конкретной группы;
- `http://localhost:8080/` — `404 Not Found`.

## Форматы generated

Генератор пишет:
- `generated/summary.json` — сводка по участникам для страницы `/standings`;
- `generated/groups.json` — список групп;
- `generated/standings/{group}.json` — готовые standings групп.

## API

- `GET /healthz`
- `GET /api/summary`
- `GET /api/groups`
- `GET /api/groups/{group_name}/standings`

## Интеграции сайтов

Реализованы клиенты:
- `internal/sites/informatics.go`
- `internal/sites/codeforces.go`
- `internal/sites/acmp.go`

Примечания:
- `informatics` использует вход через Moodle (`/login/index.php`) и получает посылки через `/py/problem/0/filter-runs` (без UI-пагинации);
- `codeforces` использует API `user.status`;
- `acmp` парсит страницу пользователя `index.asp?main=user&id=...`.

## Как добавить новый сайт

1. Добавить реализацию `SiteClient` в `internal/sites/<site>.go`.
2. Убедиться, что клиент возвращает URL задач в стабильном формате для корректной нормализации (`NormalizeTaskURL`) и совпадения с URL в `data/contests.json`.
3. Зарегистрировать клиент в `cmd/generate/main.go`.
4. Добавить аккаунты сайта в `data/students.json` (`site` + `account_id`).
5. Если интеграции нужен логин/токен, добавить загрузку конфига/секретов в `cmd/generate/main.go` (по аналогии с `-informatics-creds`).
6. Опционально: добавить читаемое название сайта для сводной таблицы в `internal/web/templates.go` (`siteTitle`).
7. Перезапустить `go run ./cmd/generate`.
