package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

const defaultInformaticsBaseURL = "https://informatics.msk.ru"

// informaticsRunsPageSize — размер страницы прогонов. Сервер informatics режет
// count по максимуму 100, поэтому запрашивать больше смысла нет: иначе номера
// страниц не совпадают с реальными офсетами, а это ломает «спасение» битой
// страницы более мелкими запросами (см. salvageFailedRunsPage).
const informaticsRunsPageSize = 100

// informaticsMaxSalvageLostRuns — если при дроблении битой страницы не удалось
// открыть больше стольки одиночных записей, считаем это не «битыми записями», а
// сбоем/недоступностью: состояние не сохраняем и перечитаем аккаунт в следующий
// раз, чтобы не пропустить их навсегда по ошибке.
const informaticsMaxSalvageLostRuns = 30
const informaticsRunsParallelism = 8

var errInformaticsNotAuthorized = errors.New("informatics: not authorized")

var logintokenRe = regexp.MustCompile(`name="logintoken"\s+value="([^"]+)"`)

type InformaticsCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	BaseURL  string `json:"base_url,omitempty"`
}

func LoadInformaticsCredentials(path string) (InformaticsCredentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return InformaticsCredentials{}, fmt.Errorf("read informatics credentials: %w", err)
	}

	var creds InformaticsCredentials
	if err := json.Unmarshal(b, &creds); err != nil {
		return InformaticsCredentials{}, fmt.Errorf("decode informatics credentials: %w", err)
	}

	creds.Username = strings.TrimSpace(creds.Username)
	creds.Password = strings.TrimSpace(creds.Password)
	creds.BaseURL = strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	if creds.BaseURL == "" {
		creds.BaseURL = defaultInformaticsBaseURL
	}

	if creds.Username == "" || creds.Password == "" {
		return InformaticsCredentials{}, errors.New("informatics credentials require username and password")
	}
	return creds, nil
}

type InformaticsAPIClient struct {
	baseURL    string
	creds      InformaticsCredentials
	httpClient *http.Client

	loginMu   sync.Mutex
	loggedIn  bool
	lastLogin time.Time

	statePath    string
	stateMu      sync.Mutex
	stateLoaded  bool
	accountState map[string]informaticsAccountState

	// Кэш «состава задач»: оглавления сборников и названия отдельных задач.
	// Меняются редко, поэтому переживают процессы (файл) и по умолчанию
	// обновляются не чаще informaticsTasksCacheTTL; tasksRefresh=true (кнопка
	// «обновить задачи») принудительно перечитывает всё с сайта.
	tasksCachePath string
	tasksRefresh   bool
	tcMu           sync.Mutex
	tcLoaded       bool
	tasksCache     informaticsTasksCacheFile
}

// informaticsTasksCacheTTL — как долго доверяем закэшированному составу
// сборника/названию задачи без перечитывания сайта.
const informaticsTasksCacheTTL = 24 * time.Hour

type informaticsTasksCacheFile struct {
	Statements map[string]informaticsCachedStatement `json:"statements,omitempty"`
	Titles     map[string]informaticsCachedTitle     `json:"titles,omitempty"`
}

type informaticsCachedStatement struct {
	Problems  []InformaticsStatementProblem `json:"problems"`
	FetchedAt time.Time                     `json:"fetched_at"`
}

type informaticsCachedTitle struct {
	Title     string    `json:"title"`
	FetchedAt time.Time `json:"fetched_at"`
}

// ConfigureTasksCache включает дисковый кэш состава задач (path) и, при
// refresh=true, принудительное перечитывание с сайта (кэш при этом обновляется).
func (c *InformaticsAPIClient) ConfigureTasksCache(path string, refresh bool) {
	c.tasksCachePath = strings.TrimSpace(path)
	c.tasksRefresh = refresh
}

func NewInformaticsAPIClientFromFileWithState(path string, statePath string) (*InformaticsAPIClient, error) {
	creds, err := LoadInformaticsCredentials(path)
	if err != nil {
		return nil, err
	}
	return NewInformaticsAPIClientWithState(creds, statePath)
}

func NewInformaticsAPIClientWithState(creds InformaticsCredentials, statePath string) (*InformaticsAPIClient, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultInformaticsBaseURL
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create cookie jar: %w", err)
	}

	return &InformaticsAPIClient{
		baseURL: baseURL,
		creds: InformaticsCredentials{
			Username: strings.TrimSpace(creds.Username),
			Password: strings.TrimSpace(creds.Password),
			BaseURL:  baseURL,
		},
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		},
		statePath:    strings.TrimSpace(statePath),
		accountState: make(map[string]informaticsAccountState),
	}, nil
}

func (c *InformaticsAPIClient) FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil
	}
	if _, err := strconv.Atoi(accountID); err != nil {
		return nil, fmt.Errorf("informatics account_id must be numeric: %w", err)
	}

	if err := c.ensureLoggedIn(ctx, false); err != nil {
		return c.staleResultsOrError(accountID, err)
	}

	state, hasState, err := c.getAccountState(accountID)
	if err != nil {
		return nil, err
	}

	lastKnownRunID := 0
	if hasState {
		lastKnownRunID = state.MaxRunID
	}

	// Забираем страницы до первой уже известной посылки. Страницы содержат и
	// известные посылки — они ПЕРЕЗАПИСЫВАЮТ сохранённые записи: последняя
	// информация с сайта — самая актуальная (вердикт мог измениться после
	// перетестирования или дотестирования из очереди).
	fetchedRuns, lostRuns, err := c.collectNewInformaticsRuns(ctx, accountID, lastKnownRunID)
	if err != nil {
		return c.staleResultsOrError(accountID, err)
	}

	runsByID := make(map[int]informaticsStoredRun, len(state.Runs)+len(fetchedRuns))
	for _, run := range state.Runs {
		if run.ID > 0 {
			runsByID[run.ID] = run
		}
	}
	maxRunID := lastKnownRunID
	for _, run := range fetchedRuns {
		if run.ID <= 0 {
			continue
		}
		runsByID[run.ID] = storedRunFromInformaticsRun(run)
		if run.ID > maxRunID {
			maxRunID = run.ID
		}
	}

	allRuns := make([]informaticsStoredRun, 0, len(runsByID))
	for _, run := range runsByID {
		allRuns = append(allRuns, run)
	}
	sort.Slice(allRuns, func(i, j int) bool { return allRuns[i].ID < allRuns[j].ID })

	aggByTask := make(map[string]informaticsTaskAggregate)
	foldStoredInformaticsRuns(allRuns, aggByTask, c.buildTaskURL)
	results := aggregatesToTaskResults(aggByTask)

	// Состояние сохраняем, если забор по сути полный: либо всё скачалось, либо
	// потеряны лишь единичные «битые» записи (informatics стабильно 500-ит на
	// конкретных прогонах — их не открыть в принципе, поэтому водяной знак можно
	// двигать: повторный перечит их всё равно не вернёт). Если недоступных записей
	// подозрительно много — это, скорее, сбой/недоступность: состояние НЕ сохраняем
	// и перечитаем аккаунт в следующий раз, чтобы не пропустить их навсегда.
	tooMuchLost := lostRuns > informaticsMaxSalvageLostRuns
	if !tooMuchLost && (len(fetchedRuns) > 0 || !hasState) {
		newState := informaticsAccountState{
			MaxRunID:  maxRunID,
			Runs:      allRuns,
			UpdatedAt: time.Now().UTC(),
		}
		if saveErr := c.saveAccountState(accountID, newState); saveErr != nil {
			return nil, saveErr
		}
	}
	if lostRuns > 0 {
		note := "пропущены (остальное забрано)"
		if tooMuchLost {
			note = "слишком много — вероятно сбой, состояние не сохранено, перечитаем позже"
		}
		log.Printf("WARN informatics account_id=%s: %d посылок недоступны на сервере (битые записи); %s", accountID, lostRuns, note)
	}

	return results, nil
}

// staleResultsOrError — informatics недоступен (не залогиниться / не забрать
// посылки): если по аккаунту есть сохранённое состояние, отдаём результаты из
// него — старые посылки лучше нулей, иначе перегенерация затёрла бы таблицы
// пустыми результатами. Без состояния (новый аккаунт) — возвращаем ошибку:
// builder пропустит аккаунт, не записав пустоту в кэш. Отмена генерации
// (context) — не сбой сайта, пробрасывается как есть.
func (c *InformaticsAPIClient) staleResultsOrError(accountID string, cause error) ([]TaskResult, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return nil, cause
	}
	state, hasState, err := c.getAccountState(accountID)
	if err != nil || !hasState {
		return nil, cause
	}
	runs := append([]informaticsStoredRun(nil), state.Runs...)
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	aggByTask := make(map[string]informaticsTaskAggregate)
	foldStoredInformaticsRuns(runs, aggByTask, c.buildTaskURL)
	log.Printf("WARN informatics account_id=%s недоступен (%v); отдаю кэш посылок от %s", accountID, cause, state.UpdatedAt.Format(time.RFC3339))
	return aggregatesToTaskResults(aggByTask), nil
}

func (c *InformaticsAPIClient) SupportsTaskScores() bool {
	return true
}

// BaseURL — настроенный адрес informatics (из base_url кредов). Билд по нему
// переписывает хост всех видимых informatics-ссылок на выбранное зеркало.
func (c *InformaticsAPIClient) BaseURL() string {
	return c.baseURL
}

func (c *InformaticsAPIClient) MatchTaskURL(taskURL string) bool {
	u, err := url.Parse(strings.TrimSpace(taskURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "informatics.msk.ru", "www.informatics.msk.ru",
		"informatics.mccme.ru", "www.informatics.mccme.ru": // старое зеркало того же сайта
	default:
		return false
	}
	return strings.EqualFold(strings.TrimSpace(u.Path), "/mod/statements/view.php")
}

func (c *InformaticsAPIClient) fetchRunsPageWithRelogin(ctx context.Context, accountID string, page int) (informaticsRunsResponse, error) {
	return c.fetchRunsPageSizedWithRelogin(ctx, accountID, informaticsRunsPageSize, page)
}

func (c *InformaticsAPIClient) fetchRunsPageSizedWithRelogin(ctx context.Context, accountID string, count, page int) (informaticsRunsResponse, error) {
	resp, err := c.fetchRunsPageSized(ctx, accountID, count, page)
	if !errors.Is(err, errInformaticsNotAuthorized) {
		return resp, err
	}

	if loginErr := c.ensureLoggedIn(ctx, true); loginErr != nil {
		return informaticsRunsResponse{}, loginErr
	}
	return c.fetchRunsPageSized(ctx, accountID, count, page)
}

// fetchRemainingRunsPages качает страницы 2..pageCount параллельно. Ошибка
// отдельной страницы (после ретраев — обычно стабильный 500 у informatics на
// самой глубокой странице) НЕ роняет весь забор: возвращаются успешно скачанные
// страницы и отсортированный список номеров непрочитанных. Реальная отмена
// контекста (ctx) прекращает всё и возвращается как ошибка.
func (c *InformaticsAPIClient) fetchRemainingRunsPages(ctx context.Context, accountID string, pageCount int) ([]informaticsRunsResponse, []int, error) {
	if pageCount <= 1 {
		return nil, nil, nil
	}

	type pageResult struct {
		page int
		resp informaticsRunsResponse
		err  error
	}

	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pages := make(chan int)
	results := make(chan pageResult, pageCount-1)

	workers := informaticsRunsParallelism
	if workers > pageCount-1 {
		workers = pageCount - 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for page := range pages {
				resp, err := c.fetchRunsPageWithRelogin(fetchCtx, accountID, page)
				select {
				case results <- pageResult{page: page, resp: resp, err: err}:
				case <-fetchCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(pages)
		for page := 2; page <= pageCount; page++ {
			select {
			case pages <- page:
			case <-fetchCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]informaticsRunsResponse, 0, pageCount-1)
	var failed []int
	for res := range results {
		if res.err != nil {
			// Реальная отмена контекста — прекращаем весь забор.
			if ctx.Err() != nil {
				return nil, nil, fmt.Errorf("fetch informatics runs page=%d: %w", res.page, res.err)
			}
			// Иначе — конкретная страница недоступна (стабильный 500 и т.п.):
			// запоминаем и продолжаем с остальными.
			failed = append(failed, res.page)
			continue
		}
		out = append(out, res.resp)
	}
	sort.Ints(failed)

	return out, failed, nil
}

// collectNewInformaticsRuns собирает посылки новее lastKnownRunID (при отсутствии
// состояния — все), не сворачивая их: решение, что запоминать, принимается выше,
// после того как известен порог незавершённых посылок. Пейджинг с ранним выходом
// сохранён: список новостей идёт от свежих к старым, поэтому на первой известной
// посылке дальнейшие страницы не запрашиваются.
//
// Второе возвращаемое значение lostRuns — сколько отдельных прогонов так и не
// удалось открыть (informatics стабильно 500-ит на конкретных «битых» записях)
// даже после дробления страницы на одиночные запросы. Все прочие прогоны
// возвращаются; решение о сохранении состояния принимается выше по lostRuns.
func (c *InformaticsAPIClient) collectNewInformaticsRuns(ctx context.Context, accountID string, lastKnownRunID int) ([]informaticsRun, int, error) {
	firstPage, err := c.fetchRunsPageWithRelogin(ctx, accountID, 1)
	if err != nil {
		return nil, 0, err
	}

	lostRuns := 0
	stopOnKnownRunID := lastKnownRunID > 0
	out := make([]informaticsRun, 0, len(firstPage.Data))
	staleReached := appendNewInformaticsRuns(&out, firstPage.Data, lastKnownRunID, stopOnKnownRunID)

	pageCount := firstPage.Metadata.PageCount
	if pageCount > 1 && len(firstPage.Data) > 0 && !staleReached {
		if stopOnKnownRunID {
			for page := 2; page <= pageCount; page++ {
				resp, pageErr := c.fetchRunsPageWithRelogin(ctx, accountID, page)
				if pageErr != nil {
					// Реальная отмена — пробрасываем; иначе страница недоступна:
					// пытаемся спасти её по мелким запросам и идём дальше искать
					// известную посылку (не прерываемся, чтобы не потерять более
					// старые, но открывающиеся страницы).
					if ctx.Err() != nil {
						return nil, 0, pageErr
					}
					lostRuns += c.salvageFailedRunsPage(ctx, accountID, informaticsRunsPageSize, page, &out)
					continue
				}
				if appendNewInformaticsRuns(&out, resp.Data, lastKnownRunID, true) {
					break
				}
			}
		} else {
			otherPages, failed, pageErr := c.fetchRemainingRunsPages(ctx, accountID, pageCount)
			if pageErr != nil {
				return nil, 0, pageErr
			}
			for _, page := range otherPages {
				appendNewInformaticsRuns(&out, page.Data, 0, false)
			}
			for _, page := range failed {
				lostRuns += c.salvageFailedRunsPage(ctx, accountID, informaticsRunsPageSize, page, &out)
			}
		}
	}

	return out, lostRuns, nil
}

// salvageFailedRunsPage пытается забрать прогоны упавшей страницы, дробя её на
// более мелкие запросы (count/10 вплоть до 1). Так теряются только реально
// неоткрывающиеся одиночные записи, а не весь блок в ~100 прогонов. Сервер
// informatics режет count по 100 и поддерживает офсеты, поэтому страница page при
// размере count покрывает ровно те же прогоны, что sub-страницы меньшего размера.
// Возвращает число безвозвратно потерянных одиночных записей.
func (c *InformaticsAPIClient) salvageFailedRunsPage(ctx context.Context, accountID string, count, page int, out *[]informaticsRun) int {
	if count <= 1 {
		return 1 // одиночная запись не открывается — теряем только её
	}
	finer := count / 10
	if finer < 1 {
		finer = 1
	}
	ratio := count / finer
	firstSub := (page-1)*ratio + 1
	lastSub := page * ratio

	lost := 0
	for sub := firstSub; sub <= lastSub; sub++ {
		resp, err := c.fetchRunsPageSizedWithRelogin(ctx, accountID, finer, sub)
		if err != nil {
			if ctx.Err() != nil {
				return lost
			}
			lost += c.salvageFailedRunsPage(ctx, accountID, finer, sub, out)
			continue
		}
		appendNewInformaticsRuns(out, resp.Data, 0, false)
	}
	return lost
}

// appendNewInformaticsRuns добавляет ВСЕ посылки страницы (включая уже
// известные — их свежие данные перезапишут сохранённые записи) и сообщает,
// достигнута ли уже известная посылка (дальше листать не нужно).
func appendNewInformaticsRuns(out *[]informaticsRun, runs []informaticsRun, lastKnownRunID int, stopOnKnownRunID bool) (staleReached bool) {
	for _, run := range runs {
		if run.ID <= 0 {
			continue
		}
		*out = append(*out, run)
		if stopOnKnownRunID && lastKnownRunID > 0 && run.ID <= lastKnownRunID {
			staleReached = true
		}
	}
	return staleReached
}

// isInformaticsPendingStatus сообщает, что посылка ещё судится и её вердикт
// может измениться (в т.ч. стать OK). Такие посылки не запоминаем. Это узкий
// набор транзиентных кодов ejudge — 11 (RUN_PENDING, «ожидает проверки»/в
// очереди) и 96..99 (RUN_RUNNING/RUN_COMPILED/RUN_COMPILING/RUN_AVAILABLE:
// выполняется/компилируется). Всё остальное — финальные вердикты. Диапазон
// намеренно узкий: «всё, что >= 95» ошибочно, у informatics встречаются
// финальные статусы с большими кодами (напр. 520), и их нельзя терять.
func isInformaticsPendingStatus(ejudgeStatus int) bool {
	return ejudgeStatus == 11 || (ejudgeStatus >= 96 && ejudgeStatus <= 99)
}

// parseInformaticsTime разбирает create_time посылки. Сейчас API отдаёт
// RFC3339 с честным "+00:00" (время в UTC; их фронтенд просто отрезает сдвиг,
// поэтому на сайте время выглядит «минус 3 часа от МСК»). На случай смены
// формата на наивный (без сдвига) интерпретируем его тоже как UTC — иначе
// посылки молча потеряют время и окно/заморозка перестанут фильтровать.
func parseInformaticsTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), true
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if t, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func inferInformaticsScore(run informaticsRun) int {
	if run.EjudgeScore.Valid {
		return domain.ClampScore(run.EjudgeScore.Value)
	}
	if run.Score.Valid {
		return domain.ClampScore(run.Score.Value)
	}
	if isInformaticsSolvedStatus(run.EjudgeStatus) {
		return 100
	}
	return 0
}

// Коды вердиктов ejudge (значения поля status в API; informatics — тот же ejudge).
// Значения см. в справке ejudge (filter_expr), константы RUN_*.
const (
	informaticsStatusOK            = 0  // RUN_OK — полный OK
	informaticsStatusAccepted      = 8  // RUN_ACCEPTED — «Зачтено» (не полный OK)
	informaticsStatusPendingReview = 16 // RUN_PENDING_REVIEW — «Ожидает подтверждения»
)

// informatics: решено (зелёный плюс) — полный OK (0) и «зачтено» (8).
func isInformaticsSolvedStatus(ejudgeStatus int) bool {
	return ejudgeStatus == informaticsStatusOK || ejudgeStatus == informaticsStatusAccepted
}

// informatics: жёлтая рамка — у «зачтено» (RUN_ACCEPTED=8), не полный OK.
func isInformaticsBorderStatus(ejudgeStatus int) bool {
	return ejudgeStatus == informaticsStatusAccepted
}

// informatics: полный OK (0) «перебивает» зачтено — тогда рамки нет.
func isInformaticsSuppressStatus(ejudgeStatus int) bool {
	return ejudgeStatus == informaticsStatusOK
}

func aggregatesToTaskResults(aggByTask map[string]informaticsTaskAggregate) []TaskResult {
	out := make([]TaskResult, 0, len(aggByTask))
	for taskURL, agg := range aggByTask {
		if !agg.attempted && !agg.solved {
			continue
		}

		var score *int
		if agg.hasScore {
			value := domain.ClampScore(agg.score)
			score = &value
		}

		timed := agg.timed
		sort.SliceStable(timed, func(i, j int) bool {
			return timed[i].At.Before(timed[j].At)
		})

		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: agg.attempted,
			Solved:    agg.solved,
			Accepted:  agg.acceptedSolved && !agg.okSolved,
			Score:     score,
			Timed:     timed,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].TaskURL < out[j].TaskURL
	})
	return out
}

func (c *InformaticsAPIClient) getAccountState(accountID string) (informaticsAccountState, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if err := c.loadStateLocked(); err != nil {
		return informaticsAccountState{}, false, err
	}

	state, ok := c.accountState[accountID]
	return state, ok, nil
}

func (c *InformaticsAPIClient) saveAccountState(accountID string, state informaticsAccountState) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if err := c.loadStateLocked(); err != nil {
		return err
	}

	c.accountState[accountID] = state
	return c.persistStateLocked()
}

func (c *InformaticsAPIClient) loadStateLocked() error {
	if c.stateLoaded {
		return nil
	}

	if c.statePath == "" {
		c.stateLoaded = true
		return nil
	}

	b, err := os.ReadFile(c.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.stateLoaded = true
			return nil
		}
		return fmt.Errorf("read informatics state %q: %w", c.statePath, err)
	}

	var decoded informaticsStateFile
	if err := json.Unmarshal(b, &decoded); err != nil {
		return fmt.Errorf("decode informatics state %q: %w", c.statePath, err)
	}
	if decoded.Version != informaticsStateVersion || decoded.Accounts == nil {
		// Старая/несовместимая версия кэша — игнорируем и пересчитываем заново.
		decoded.Accounts = make(map[string]informaticsAccountState)
	}

	c.accountState = decoded.Accounts
	c.stateLoaded = true
	return nil
}

func (c *InformaticsAPIClient) persistStateLocked() error {
	if c.statePath == "" {
		return nil
	}

	state := informaticsStateFile{Version: informaticsStateVersion, Accounts: c.accountState}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal informatics state: %w", err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o755); err != nil {
		return fmt.Errorf("mkdir informatics state dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(c.statePath), "informatics-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(b); err != nil {
		tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp state file: %w", err)
	}

	if err := os.Rename(tmpPath, c.statePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename state file: %w", err)
	}
	return nil
}

func (c *InformaticsAPIClient) ensureLoggedIn(ctx context.Context, force bool) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	if !force && c.loggedIn && time.Since(c.lastLogin) < 30*time.Minute {
		return nil
	}

	if err := c.loginLocked(ctx); err != nil {
		c.loggedIn = false
		return err
	}
	c.loggedIn = true
	c.lastLogin = time.Now()
	return nil
}

func (c *InformaticsAPIClient) loginLocked(ctx context.Context) error {
	loginURL := c.baseURL + "/login/index.php"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loginURL, nil)
	if err != nil {
		return err
	}
	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	res.Body.Close()
	if err != nil {
		return err
	}

	match := logintokenRe.FindStringSubmatch(string(body))
	if len(match) != 2 {
		return errors.New("informatics login token not found")
	}
	logintoken := match[1]

	form := url.Values{}
	form.Set("anchor", "")
	form.Set("logintoken", logintoken)
	form.Set("username", c.creds.Username)
	form.Set("password", c.creds.Password)
	form.Set("rememberusername", "0")

	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postReq.Header.Set("Referer", loginURL)

	postRes, err := c.httpClient.Do(postReq)
	if err != nil {
		return err
	}
	postBody, err := io.ReadAll(io.LimitReader(postRes.Body, 2<<20))
	postRes.Body.Close()
	if err != nil {
		return err
	}

	bodyText := string(postBody)
	if strings.Contains(bodyText, "logout.php") {
		return nil
	}
	if strings.Contains(bodyText, "name=\"logintoken\"") || strings.Contains(bodyText, "action=\"https://informatics.msk.ru/login/index.php\"") || strings.Contains(bodyText, "loginerrors") {
		return errors.New("informatics login failed: bad credentials or blocked account")
	}
	return nil
}

func (c *InformaticsAPIClient) fetchRunsPage(ctx context.Context, accountID string, page int) (informaticsRunsResponse, error) {
	return c.fetchRunsPageSized(ctx, accountID, informaticsRunsPageSize, page)
}

func (c *InformaticsAPIClient) fetchRunsPageSized(ctx context.Context, accountID string, count, page int) (informaticsRunsResponse, error) {
	u, err := url.Parse(c.baseURL + "/py/problem/0/filter-runs")
	if err != nil {
		return informaticsRunsResponse{}, err
	}

	q := u.Query()
	q.Set("problem_id", "0")
	q.Set("user_id", accountID)
	q.Set("count", strconv.Itoa(count))
	q.Set("page", strconv.Itoa(page))
	q.Set("from_timestamp", "-1")
	q.Set("to_timestamp", "-1")
	q.Set("lang_id", "-1")
	q.Set("status_id", "-1")
	q.Set("statement_id", "0")
	q.Set("with_comment", "")
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return informaticsRunsResponse{}, err
	}

	res, err := doHTTPWithRetry(c.httpClient, req, defaultHTTPRetryAttempts)
	if err != nil {
		return informaticsRunsResponse{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return informaticsRunsResponse{}, fmt.Errorf("informatics runs status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded informaticsRunsResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return informaticsRunsResponse{}, err
	}

	if decoded.Result == "error" && strings.EqualFold(decoded.Message, "Not authorized") {
		return informaticsRunsResponse{}, errInformaticsNotAuthorized
	}
	if decoded.Result != "success" {
		return informaticsRunsResponse{}, fmt.Errorf("informatics runs error: %s", strings.TrimSpace(decoded.Message))
	}

	return decoded, nil
}

func (c *InformaticsAPIClient) buildTaskURL(problemID int) string {
	if problemID <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/mod/statements/view.php?chapterid=%d#1", c.baseURL, problemID)
}

// InformaticsStatementProblem — задача сборника informatics: chapterid (он же id
// задачи в посылках) и заголовок из оглавления страницы.
type InformaticsStatementProblem struct {
	ChapterID int
	Title     string
	// Hidden — задача скрыта в сборнике (в оглавлении затемнена, класс
	// dimmed_text). Ученикам не показывается, по токену жюри — показывается.
	Hidden bool
}

// ParseInformaticsStatementID распознаёт ссылку на СБОРНИК informatics —
// mod/statements/view.php?id=<N> без chapterid (страница-контейнер с оглавлением
// задач). Возвращает (id, true). Ссылка с chapterid — это конкретная задача, а
// не сборник, поэтому (0, false).
func ParseInformaticsStatementID(rawURL string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "informatics.msk.ru", "www.informatics.msk.ru",
		"informatics.mccme.ru", "www.informatics.mccme.ru":
	default:
		return 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(u.Path), "/mod/statements/view.php") {
		return 0, false
	}
	q := u.Query()
	if strings.TrimSpace(q.Get("chapterid")) != "" {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(q.Get("id")))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

var (
	// Оглавление сборника: блок statements_toc → его <ul>…</ul>.
	informaticsTocRe = regexp.MustCompile(`(?is)statements_toc.*?<ul>(.*?)</ul>`)
	informaticsLiRe  = regexp.MustCompile(`(?is)<li>(.*?)</li>`)
	// chapterid из ссылки пункта оглавления.
	informaticsChapterHrefRe = regexp.MustCompile(`chapterid=(\d+)`)
	// id активной (открытой) задачи — у неё в оглавлении нет ссылки, chapterid
	// берём из скрытого блока problem_data.
	informaticsActiveProblemRe = regexp.MustCompile(`(?is)problem_data[^>]*problem_id=['"](\d+)['"]`)
	informaticsTagRe           = regexp.MustCompile(`(?is)<[^>]+>`)
	informaticsWSRe            = regexp.MustCompile(`\s+`)
	// Скрытая (затемнённая) задача сборника — название в <span class="dimmed_text">.
	informaticsHiddenProblemRe = regexp.MustCompile(`(?i)dimmed_text`)
	// Заголовок страницы задачи: «<title>Задача №1209. Клавиатура</title>» —
	// вытаскиваем название после «Задача №N.».
	informaticsTaskTitleRe = regexp.MustCompile(`(?is)<title>\s*Задача\s*№\s*\d+\s*\.?\s*(.*?)\s*</title>`)
)

// ParseInformaticsTaskChapterID распознаёт ссылку на ОТДЕЛЬНУЮ задачу informatics
// (mod/statements/view.php?chapterid=<N>) и возвращает её chapterid. Ссылка на
// сборник (id без chapterid) сюда не подходит — для неё есть отдельный разбор.
func ParseInformaticsTaskChapterID(rawURL string) (int, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(u.Hostname()) {
	case "informatics.msk.ru", "www.informatics.msk.ru",
		"informatics.mccme.ru", "www.informatics.mccme.ru":
	default:
		return 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(u.Path), "/mod/statements/view.php") {
		return 0, false
	}
	id, err := strconv.Atoi(strings.TrimSpace(u.Query().Get("chapterid")))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// FetchTaskTitle читает страницу задачи informatics и возвращает её название
// (для подсказки в заголовке колонки). Пустая строка — название не распозналось.
func (c *InformaticsAPIClient) FetchTaskTitle(ctx context.Context, chapterID int) (string, error) {
	if chapterID <= 0 {
		return "", fmt.Errorf("informatics chapter id must be > 0")
	}
	key := strconv.Itoa(chapterID)
	if !c.tasksRefresh {
		c.tcMu.Lock()
		c.loadTasksCacheLocked()
		rec, ok := c.tasksCache.Titles[key]
		c.tcMu.Unlock()
		if ok && time.Since(rec.FetchedAt) <= informaticsTasksCacheTTL {
			return rec.Title, nil
		}
	}
	if err := c.ensureLoggedIn(ctx, false); err != nil {
		return "", err
	}
	pageURL := fmt.Sprintf("%s/mod/statements/view.php?chapterid=%d", c.baseURL, chapterID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	res, err := doHTTPWithRetry(c.httpClient, req, defaultHTTPRetryAttempts)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return "", fmt.Errorf("informatics task status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	title := parseInformaticsTaskTitle(body)
	if title != "" {
		c.tcMu.Lock()
		c.loadTasksCacheLocked()
		if c.tasksCache.Titles == nil {
			c.tasksCache.Titles = make(map[string]informaticsCachedTitle)
		}
		c.tasksCache.Titles[key] = informaticsCachedTitle{Title: title, FetchedAt: time.Now().UTC()}
		c.persistTasksCacheLocked()
		c.tcMu.Unlock()
	}
	return title, nil
}

// parseInformaticsTaskTitle извлекает название задачи из <title> страницы.
func parseInformaticsTaskTitle(body []byte) string {
	m := informaticsTaskTitleRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// FetchStatementProblems разворачивает сборник informatics (id) в упорядоченный
// список задач: читает страницу сборника и разбирает оглавление. Активная
// (первая) задача показана без ссылки — её chapterid берётся из problem_data.
func (c *InformaticsAPIClient) FetchStatementProblems(ctx context.Context, statementID int) ([]InformaticsStatementProblem, error) {
	if statementID <= 0 {
		return nil, fmt.Errorf("informatics statement id must be > 0")
	}
	key := strconv.Itoa(statementID)
	if cached, ok := c.cachedStatement(key); ok {
		return cached, nil
	}

	if err := c.ensureLoggedIn(ctx, false); err != nil {
		return nil, err
	}

	body, err := c.fetchStatementPage(ctx, statementID)
	if err != nil {
		// Сайт недоступен — отдаём устаревший кэш, если он есть (лучше старый
		// состав, чем пропавшие колонки).
		if stale, ok := c.staleStatement(key); ok {
			log.Printf("WARN informatics statement id=%d: fetch failed (%v); использую кэш", statementID, err)
			return stale, nil
		}
		return nil, err
	}
	problems, err := parseInformaticsStatementProblems(body)
	if err != nil {
		if stale, ok := c.staleStatement(key); ok {
			log.Printf("WARN informatics statement id=%d: parse failed (%v); использую кэш", statementID, err)
			return stale, nil
		}
		return nil, err
	}
	if len(problems) == 0 {
		return nil, fmt.Errorf("informatics statement id=%d: no problems parsed", statementID)
	}
	c.storeStatement(key, problems)
	return problems, nil
}

// cachedStatement возвращает свежую запись кэша (учитывая TTL и tasksRefresh).
func (c *InformaticsAPIClient) cachedStatement(key string) ([]InformaticsStatementProblem, bool) {
	if c.tasksRefresh {
		return nil, false
	}
	c.tcMu.Lock()
	defer c.tcMu.Unlock()
	c.loadTasksCacheLocked()
	rec, ok := c.tasksCache.Statements[key]
	if !ok || time.Since(rec.FetchedAt) > informaticsTasksCacheTTL {
		return nil, false
	}
	return cloneStatementProblems(rec.Problems), true
}

// staleStatement — запись кэша любой давности (fallback при недоступном сайте).
func (c *InformaticsAPIClient) staleStatement(key string) ([]InformaticsStatementProblem, bool) {
	c.tcMu.Lock()
	defer c.tcMu.Unlock()
	c.loadTasksCacheLocked()
	rec, ok := c.tasksCache.Statements[key]
	if !ok {
		return nil, false
	}
	return cloneStatementProblems(rec.Problems), true
}

func (c *InformaticsAPIClient) storeStatement(key string, problems []InformaticsStatementProblem) {
	c.tcMu.Lock()
	defer c.tcMu.Unlock()
	c.loadTasksCacheLocked()
	if c.tasksCache.Statements == nil {
		c.tasksCache.Statements = make(map[string]informaticsCachedStatement)
	}
	c.tasksCache.Statements[key] = informaticsCachedStatement{Problems: cloneStatementProblems(problems), FetchedAt: time.Now().UTC()}
	c.persistTasksCacheLocked()
}

func cloneStatementProblems(in []InformaticsStatementProblem) []InformaticsStatementProblem {
	out := make([]InformaticsStatementProblem, len(in))
	copy(out, in)
	return out
}

// loadTasksCacheLocked лениво читает файл кэша (один раз на процесс).
func (c *InformaticsAPIClient) loadTasksCacheLocked() {
	if c.tcLoaded {
		return
	}
	c.tcLoaded = true
	if c.tasksCachePath == "" {
		return
	}
	b, err := os.ReadFile(c.tasksCachePath)
	if err != nil {
		return // нет файла — пустой кэш
	}
	_ = json.Unmarshal(b, &c.tasksCache)
}

// persistTasksCacheLocked атомарно сохраняет кэш (как state-файл).
func (c *InformaticsAPIClient) persistTasksCacheLocked() {
	if c.tasksCachePath == "" {
		return
	}
	b, err := json.MarshalIndent(c.tasksCache, "", "  ")
	if err != nil {
		return
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(c.tasksCachePath), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.tasksCachePath), "informatics-tasks-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmpPath)
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return
	}
	if err := os.Rename(tmpPath, c.tasksCachePath); err != nil {
		_ = os.Remove(tmpPath)
	}
}

func (c *InformaticsAPIClient) fetchStatementPage(ctx context.Context, statementID int) ([]byte, error) {
	pageURL := fmt.Sprintf("%s/mod/statements/view.php?id=%d", c.baseURL, statementID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}
	res, err := doHTTPWithRetry(c.httpClient, req, defaultHTTPRetryAttempts)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, fmt.Errorf("informatics statement status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(res.Body)
}

func parseInformaticsStatementProblems(body []byte) ([]InformaticsStatementProblem, error) {
	toc := informaticsTocRe.FindSubmatch(body)
	if toc == nil {
		return nil, fmt.Errorf("informatics statement: оглавление (statements_toc) не найдено")
	}

	activeID := 0
	if m := informaticsActiveProblemRe.FindSubmatch(body); m != nil {
		activeID, _ = strconv.Atoi(string(m[1]))
	}

	items := informaticsLiRe.FindAllSubmatch(toc[1], -1)
	out := make([]InformaticsStatementProblem, 0, len(items))
	seen := make(map[int]struct{}, len(items))
	for _, item := range items {
		li := item[1]
		// Скрытая задача сборника: в оглавлении её название затемнено
		// (<span class="dimmed_text">x …</span>). Помечаем — на публичном виде её
		// вырежет сервер, а по токену жюри задача остаётся видимой.
		hidden := informaticsHiddenProblemRe.Match(li)
		chapterID := 0
		if m := informaticsChapterHrefRe.FindSubmatch(li); m != nil {
			chapterID, _ = strconv.Atoi(string(m[1]))
		} else {
			// Пункт без ссылки — открытая (активная) задача сборника.
			chapterID = activeID
		}
		if chapterID <= 0 {
			continue
		}
		if _, dup := seen[chapterID]; dup {
			continue
		}
		seen[chapterID] = struct{}{}

		title := informaticsWSRe.ReplaceAllString(string(informaticsTagRe.ReplaceAll(li, []byte(" "))), " ")
		out = append(out, InformaticsStatementProblem{
			ChapterID: chapterID,
			Title:     strings.TrimSpace(title),
			Hidden:    hidden,
		})
	}
	return out, nil
}

type informaticsRunsResponse struct {
	Result   string           `json:"result"`
	Message  string           `json:"message"`
	Data     []informaticsRun `json:"data"`
	Metadata informaticsMeta  `json:"metadata"`
}

type informaticsMeta struct {
	PageCount int `json:"page_count"`
	Count     int `json:"count"`
}

type informaticsRun struct {
	ID           int                `json:"id"`
	EjudgeStatus int                `json:"ejudge_status"`
	EjudgeScore  maybeInt           `json:"ejudge_score"`
	Score        maybeInt           `json:"score"`
	CreateTime   string             `json:"create_time"`
	Problem      informaticsProblem `json:"problem"`
}

type informaticsProblem struct {
	ID int `json:"id"`
}

type maybeInt struct {
	Value int
	Valid bool
}

func (m *maybeInt) UnmarshalJSON(data []byte) error {
	s := strings.TrimSpace(string(data))
	if s == "" || s == "null" {
		m.Valid = false
		m.Value = 0
		return nil
	}

	if strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"") {
		unquoted, err := strconv.Unquote(s)
		if err != nil {
			m.Valid = false
			m.Value = 0
			return nil
		}
		s = strings.TrimSpace(unquoted)
	}

	if s == "" {
		m.Valid = false
		m.Value = 0
		return nil
	}

	if i, err := strconv.Atoi(s); err == nil {
		m.Valid = true
		m.Value = i
		return nil
	}

	if f, err := strconv.ParseFloat(s, 64); err == nil {
		m.Valid = true
		m.Value = int(math.Round(f))
		return nil
	}

	m.Valid = false
	m.Value = 0
	return nil
}

type informaticsTaskAggregate struct {
	attempted bool
	solved    bool
	// okSolved — был полный OK; acceptedSolved — было «зачтено» (RUN_ACCEPTED).
	// «Зачтено» (без полного OK) помечается в таблице.
	okSolved       bool
	acceptedSolved bool
	score          int
	hasScore       bool
	timed          []TimedSubmission
}

// informaticsStateVersion — версия схемы/логики кэша. При изменении правил
// трактовки вердиктов или формата версию повышаем, чтобы старый кэш
// игнорировался и результаты пересчитались с нуля.
// v6: храним сырые посылки по run_id; каждый забор перезаписывает их свежими
// данными («последняя информация — самая актуальная»), агрегаты собираются из
// сохранённых посылок заново.
const informaticsStateVersion = 6

type informaticsStateFile struct {
	Version  int                                `json:"version"`
	Accounts map[string]informaticsAccountState `json:"accounts"`
}

type informaticsAccountState struct {
	MaxRunID  int                    `json:"max_run_id"`
	Runs      []informaticsStoredRun `json:"runs,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
}

// informaticsStoredRun — сохранённая посылка. Ключ — ID: при повторном заборе
// запись перезаписывается свежими данными (вердикт мог измениться:
// перетестирование, дотестирование из очереди).
type informaticsStoredRun struct {
	ID        int       `json:"id"`
	ProblemID int       `json:"problem_id"`
	Status    int       `json:"status"`
	Score     int       `json:"score"`
	At        time.Time `json:"at,omitempty"` // нулевое — времени нет
}

// storedRunFromInformaticsRun переводит посылку API в сохранённую форму.
func storedRunFromInformaticsRun(run informaticsRun) informaticsStoredRun {
	out := informaticsStoredRun{
		ID:        run.ID,
		ProblemID: run.Problem.ID,
		Status:    run.EjudgeStatus,
		Score:     inferInformaticsScore(run),
	}
	if at, ok := parseInformaticsTime(run.CreateTime); ok {
		out.At = at
	}
	return out
}

// foldStoredInformaticsRuns собирает агрегаты по задачам из сохранённых посылок.
// Незавершённые (в очереди/тестируются) пропускаются: их финальный вердикт
// перезапишет запись при одном из следующих заборов.
func foldStoredInformaticsRuns(runs []informaticsStoredRun, aggByTask map[string]informaticsTaskAggregate, buildTaskURL func(problemID int) string) {
	for _, run := range runs {
		if run.ID <= 0 || isInformaticsPendingStatus(run.Status) {
			continue
		}
		taskURL := buildTaskURL(run.ProblemID)
		if taskURL == "" {
			continue
		}
		agg := aggByTask[taskURL]
		agg.attempted = true
		solved := isInformaticsSolvedStatus(run.Status)
		border := isInformaticsBorderStatus(run.Status)
		if solved {
			agg.solved = true
			if border {
				agg.acceptedSolved = true
			}
			if isInformaticsSuppressStatus(run.Status) {
				agg.okSolved = true
			}
		}
		if !agg.hasScore || run.Score > agg.score {
			agg.score = run.Score
			agg.hasScore = true
		}
		if !run.At.IsZero() {
			score := run.Score
			agg.timed = append(agg.timed, TimedSubmission{At: run.At, Solved: solved, Score: &score, Accepted: border})
		}
		aggByTask[taskURL] = agg
	}
}
