package source

import (
	"io"
	"net/http"
	"time"
)

// defaultHTTPRetryAttempts — сколько всего раз пытаться выполнить идемпотентный
// GET, включая первую попытку. 1–2 случайных аккаунта на informatics/codeforces
// периодически получают временный 5xx; без повтора такой аккаунт целиком выпадает
// из генерации.
const defaultHTTPRetryAttempts = 3

// httpRetryBaseDelay — базовая пауза между попытками (растёт линейно с номером).
var httpRetryBaseDelay = 400 * time.Millisecond

// isRetriableHTTPStatus — статус ответа временный и запрос имеет смысл повторить:
// 429 (слишком много запросов) и любой 5xx. 4xx (кроме 429) и 2xx не повторяем.
func isRetriableHTTPStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code <= 599)
}

// doHTTPWithRetry выполняет идемпотентный запрос с повторами при сетевых ошибках
// и временных статусах (429, 5xx). Возвращает ПОСЛЕДНИЙ ответ с открытым телом —
// закрывает его вызывающий. На последней попытке ответ с временным статусом
// возвращается как есть, чтобы вызывающий сам сформировал привычную ошибку по
// коду/телу. attempts<=0 трактуется как одна попытка.
func doHTTPWithRetry(client *http.Client, req *http.Request, attempts int) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	ctx := req.Context()

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt) * httpRetryBaseDelay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		res, err := client.Do(req.Clone(ctx))
		if err != nil {
			lastErr = err
			continue
		}
		// Временный статус и попытки ещё есть — сбрасываем тело и повторяем.
		if isRetriableHTTPStatus(res.StatusCode) && attempt < attempts-1 {
			io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
			res.Body.Close()
			continue
		}
		return res, nil
	}
	return nil, lastErr
}
