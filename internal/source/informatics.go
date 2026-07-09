package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const informaticsRunsPageSize = "1000"
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
		return nil, err
	}

	state, hasState, err := c.getAccountState(accountID)
	if err != nil {
		return nil, err
	}

	aggByTask := make(map[string]informaticsTaskAggregate)
	if hasState {
		mergeStateIntoAggregates(aggByTask, state)
	}

	lastKnownRunID := 0
	if hasState {
		lastKnownRunID = state.MaxRunID
	}

	newRuns, err := c.collectNewInformaticsRuns(ctx, accountID, lastKnownRunID)
	if err != nil {
		return nil, err
	}

	maxRunID := foldNewInformaticsRuns(newRuns, lastKnownRunID, aggByTask, c.buildTaskURL)

	results := aggregatesToTaskResults(aggByTask)

	if maxRunID > lastKnownRunID || !hasState {
		newState := informaticsAccountState{
			MaxRunID:  maxRunID,
			Results:   results,
			UpdatedAt: time.Now().UTC(),
		}
		if saveErr := c.saveAccountState(accountID, newState); saveErr != nil {
			return nil, saveErr
		}
	}

	return results, nil
}

func (c *InformaticsAPIClient) SupportsTaskScores() bool {
	return true
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
	resp, err := c.fetchRunsPage(ctx, accountID, page)
	if !errors.Is(err, errInformaticsNotAuthorized) {
		return resp, err
	}

	if loginErr := c.ensureLoggedIn(ctx, true); loginErr != nil {
		return informaticsRunsResponse{}, loginErr
	}
	return c.fetchRunsPage(ctx, accountID, page)
}

func (c *InformaticsAPIClient) fetchRemainingRunsPages(ctx context.Context, accountID string, pageCount int) ([]informaticsRunsResponse, error) {
	if pageCount <= 1 {
		return nil, nil
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
	var firstErr error
	for res := range results {
		if res.err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("fetch informatics runs page=%d: %w", res.page, res.err)
				cancel()
			}
			continue
		}
		out = append(out, res.resp)
	}
	if firstErr != nil {
		return nil, firstErr
	}

	return out, nil
}

// collectNewInformaticsRuns собирает посылки новее lastKnownRunID (при отсутствии
// состояния — все), не сворачивая их: решение, что запоминать, принимается выше,
// после того как известен порог незавершённых посылок. Пейджинг с ранним выходом
// сохранён: список новостей идёт от свежих к старым, поэтому на первой известной
// посылке дальнейшие страницы не запрашиваются.
func (c *InformaticsAPIClient) collectNewInformaticsRuns(ctx context.Context, accountID string, lastKnownRunID int) ([]informaticsRun, error) {
	firstPage, err := c.fetchRunsPageWithRelogin(ctx, accountID, 1)
	if err != nil {
		return nil, err
	}

	stopOnKnownRunID := lastKnownRunID > 0
	out := make([]informaticsRun, 0, len(firstPage.Data))
	staleReached := appendNewInformaticsRuns(&out, firstPage.Data, lastKnownRunID, stopOnKnownRunID)

	pageCount := firstPage.Metadata.PageCount
	if pageCount > 1 && len(firstPage.Data) > 0 && !staleReached {
		if stopOnKnownRunID {
			for page := 2; page <= pageCount; page++ {
				resp, pageErr := c.fetchRunsPageWithRelogin(ctx, accountID, page)
				if pageErr != nil {
					return nil, pageErr
				}
				if appendNewInformaticsRuns(&out, resp.Data, lastKnownRunID, true) {
					break
				}
			}
		} else {
			otherPages, pageErr := c.fetchRemainingRunsPages(ctx, accountID, pageCount)
			if pageErr != nil {
				return nil, pageErr
			}
			for _, page := range otherPages {
				appendNewInformaticsRuns(&out, page.Data, 0, false)
			}
		}
	}

	return out, nil
}

func appendNewInformaticsRuns(out *[]informaticsRun, runs []informaticsRun, lastKnownRunID int, stopOnKnownRunID bool) (staleReached bool) {
	for _, run := range runs {
		if run.ID <= 0 {
			continue
		}
		if lastKnownRunID > 0 && run.ID <= lastKnownRunID {
			if stopOnKnownRunID {
				return true
			}
			continue
		}
		*out = append(*out, run)
	}
	return false
}

// foldNewInformaticsRuns сворачивает завершённые новые посылки в агрегаты и
// возвращает новый водяной знак (maxRunID). Незавершённые посылки (идёт проверка/
// в очереди) и всё не старше самой ранней из них пропускаются и в состояние не
// попадают — их повторно подхватят в следующий сбор, когда вердикт станет
// финальным (иначе, став OK, они были бы пропущены как «уже известные»).
func foldNewInformaticsRuns(newRuns []informaticsRun, lastKnownRunID int, aggByTask map[string]informaticsTaskAggregate, buildTaskURL func(problemID int) string) (maxRunID int) {
	pendingFloor := 0
	for _, run := range newRuns {
		if run.ID > 0 && isInformaticsPendingStatus(run.EjudgeStatus) {
			if pendingFloor == 0 || run.ID < pendingFloor {
				pendingFloor = run.ID
			}
		}
	}

	maxRunID = lastKnownRunID
	for _, run := range newRuns {
		if run.ID <= 0 {
			continue
		}
		if pendingFloor > 0 && run.ID >= pendingFloor {
			continue
		}
		foldInformaticsRun(run, aggByTask, buildTaskURL)
		if run.ID > maxRunID {
			maxRunID = run.ID
		}
	}
	return maxRunID
}

func foldInformaticsRun(run informaticsRun, aggByTask map[string]informaticsTaskAggregate, buildTaskURL func(problemID int) string) {
	taskURL := buildTaskURL(run.Problem.ID)
	if taskURL == "" {
		return
	}

	agg := aggByTask[taskURL]
	agg.attempted = true
	solved := isInformaticsSolvedStatus(run.EjudgeStatus)
	accepted := solved && isInformaticsAcceptedStatus(run.EjudgeStatus)
	if solved {
		agg.solved = true
		if accepted {
			agg.acceptedSolved = true
		} else {
			agg.okSolved = true
		}
	}

	score := inferInformaticsScore(run)
	if !agg.hasScore || score > agg.score {
		agg.score = score
		agg.hasScore = true
	}

	if at, ok := parseInformaticsTime(run.CreateTime); ok {
		submissionScore := score
		agg.timed = append(agg.timed, TimedSubmission{At: at, Solved: solved, Score: &submissionScore, Accepted: accepted})
	}

	aggByTask[taskURL] = agg
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

// ejudge run status: 0 = OK, 8 = RUN_ACCEPTED («Зачтено»/«Принято»).
// Оба означают, что задача решена, и должны трактоваться одинаково.
const (
	informaticsStatusOK       = 0
	informaticsStatusAccepted = 8
)

func isInformaticsSolvedStatus(ejudgeStatus int) bool {
	return ejudgeStatus == informaticsStatusOK || ejudgeStatus == informaticsStatusAccepted
}

// isInformaticsAcceptedStatus — «зачтено» (RUN_ACCEPTED=8), не полный OK.
func isInformaticsAcceptedStatus(ejudgeStatus int) bool {
	return ejudgeStatus == informaticsStatusAccepted
}

func mergeStateIntoAggregates(aggByTask map[string]informaticsTaskAggregate, state informaticsAccountState) {
	for _, task := range state.Solved {
		url := strings.TrimSpace(task)
		if url == "" {
			continue
		}
		agg := aggByTask[url]
		agg.attempted = true
		agg.solved = true
		if !agg.hasScore || 100 > agg.score {
			agg.score = 100
			agg.hasScore = true
		}
		aggByTask[url] = agg
	}

	for _, task := range state.Attempted {
		url := strings.TrimSpace(task)
		if url == "" {
			continue
		}
		agg := aggByTask[url]
		agg.attempted = true
		if !agg.hasScore {
			agg.score = 0
			agg.hasScore = true
		}
		aggByTask[url] = agg
	}

	for _, result := range state.Results {
		url := strings.TrimSpace(result.TaskURL)
		if url == "" {
			continue
		}

		agg := aggByTask[url]
		attempted := result.Attempted || result.Solved || result.Score != nil
		if attempted {
			agg.attempted = true
		}
		if result.Solved {
			agg.solved = true
			if result.Accepted {
				agg.acceptedSolved = true
			} else {
				agg.okSolved = true
			}
		}

		if result.Score != nil {
			score := domain.ClampScore(*result.Score)
			if !agg.hasScore || score > agg.score {
				agg.score = score
				agg.hasScore = true
			}
		}
		if len(result.Timed) > 0 {
			agg.timed = append(agg.timed, cloneTimedSubmissions(result.Timed)...)
		}
		aggByTask[url] = agg
	}

	for taskURL, agg := range aggByTask {
		if agg.attempted && !agg.hasScore {
			if agg.solved {
				agg.score = 100
			} else {
				agg.score = 0
			}
			agg.hasScore = true
			aggByTask[taskURL] = agg
		}
	}
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

	state.Solved = nil
	state.Attempted = nil
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
	u, err := url.Parse(c.baseURL + "/py/problem/0/filter-runs")
	if err != nil {
		return informaticsRunsResponse{}, err
	}

	q := u.Query()
	q.Set("problem_id", "0")
	q.Set("user_id", accountID)
	q.Set("count", informaticsRunsPageSize)
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
)

// FetchStatementProblems разворачивает сборник informatics (id) в упорядоченный
// список задач: читает страницу сборника и разбирает оглавление. Активная
// (первая) задача показана без ссылки — её chapterid берётся из problem_data.
func (c *InformaticsAPIClient) FetchStatementProblems(ctx context.Context, statementID int) ([]InformaticsStatementProblem, error) {
	if statementID <= 0 {
		return nil, fmt.Errorf("informatics statement id must be > 0")
	}
	if err := c.ensureLoggedIn(ctx, false); err != nil {
		return nil, err
	}

	body, err := c.fetchStatementPage(ctx, statementID)
	if err != nil {
		return nil, err
	}
	problems, err := parseInformaticsStatementProblems(body)
	if err != nil {
		return nil, err
	}
	if len(problems) == 0 {
		return nil, fmt.Errorf("informatics statement id=%d: no problems parsed", statementID)
	}
	return problems, nil
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
// трактовки вердиктов или формата (добавление статуса «Зачтено», времени посылок)
// версию повышаем, чтобы старый кэш игнорировался и результаты пересчитались с нуля.
const informaticsStateVersion = 4

type informaticsStateFile struct {
	Version  int                                `json:"version"`
	Accounts map[string]informaticsAccountState `json:"accounts"`
}

type informaticsAccountState struct {
	MaxRunID  int          `json:"max_run_id"`
	Results   []TaskResult `json:"results,omitempty"`
	Solved    []string     `json:"solved,omitempty"`
	Attempted []string     `json:"attempted,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}
