package source

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

// EjudgeInstanceConfig — один экземпляр ejudge из data/ejudge_credentials.json.
// ejudge_id становится «сайтом» (в аккаунтах учеников и при сопоставлении по
// URL). Аутентификация — API-ключ (Bearer). Ключи в ejudge привязаны к контесту,
// поэтому можно задать общий api_key и переопределения по контестам в contest_keys.
type EjudgeInstanceConfig struct {
	EjudgeID    string            `json:"ejudge_id"`
	BaseURL     string            `json:"base_url"`
	APIBase     string            `json:"api_base,omitempty"` // по умолчанию <base_url>/ej/api/v1
	APIKey      string            `json:"api_key"`
	ContestKeys map[string]string `json:"contest_keys,omitempty"`
}

// LoadEjudgeInstances читает массив экземпляров ejudge из файла. Отсутствие файла —
// не ошибка (источник просто отключён): возвращает (nil, nil).
func LoadEjudgeInstances(path string) ([]EjudgeInstanceConfig, error) {
	body, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var instances []EjudgeInstanceConfig
	if err := json.Unmarshal(body, &instances); err != nil {
		return nil, fmt.Errorf("decode ejudge credentials %q: %w", path, err)
	}
	seen := make(map[string]struct{}, len(instances))
	for i := range instances {
		// ejudge_id — это ещё и имя сайта (сопоставление регистронезависимое),
		// поэтому приводим к нижнему регистру и требуем «чистый» идентификатор.
		id := strings.ToLower(strings.TrimSpace(instances[i].EjudgeID))
		instances[i].EjudgeID = id
		instances[i].BaseURL = strings.TrimSpace(instances[i].BaseURL)
		if id == "" {
			return nil, fmt.Errorf("ejudge credentials: empty ejudge_id at index %d", i)
		}
		if !ejudgeIDRe.MatchString(id) {
			return nil, fmt.Errorf("ejudge_id %q: только строчная латиница, цифры, дефис и подчёркивание", id)
		}
		if instances[i].BaseURL == "" {
			return nil, fmt.Errorf("ejudge %q: base_url обязателен", id)
		}
		if _, dup := seen[id]; dup {
			return nil, fmt.Errorf("ejudge_id %q встречается дважды", id)
		}
		seen[id] = struct{}{}
	}
	return instances, nil
}

var ejudgeIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// EjudgeProblem — задача контеста ejudge (для разворачивания ссылки на контест).
type EjudgeProblem struct {
	ProbID    int
	ShortName string
	LongName  string
}

type EjudgeClient struct {
	ejudgeID    string
	host        string
	baseURL     string // https://host — для построения ссылок new-client
	apiBase     string // https://host/ej/api/v1
	apiKey      string
	contestKeys map[string]string
	httpClient  *http.Client
	logger      *log.Logger

	mu       sync.Mutex
	contests map[int]struct{}        // contest_id, встреченные в ссылках задач
	fetched  bool                    // все контесты уже скачаны
	byLogin  map[string][]TaskResult // login -> результаты

	problemsMu    sync.Mutex
	problemsCache map[int][]EjudgeProblem // contest_id -> задачи (для разворачивания)
}

func NewEjudgeClient(cfg EjudgeInstanceConfig) (*EjudgeClient, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("ejudge %q: bad base_url %q", cfg.EjudgeID, cfg.BaseURL)
	}
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if apiBase == "" {
		apiBase = base + "/ej/api/v1"
	}
	return &EjudgeClient{
		ejudgeID:    cfg.EjudgeID,
		host:        strings.ToLower(u.Hostname()),
		baseURL:     base,
		apiBase:     apiBase,
		apiKey:      strings.TrimSpace(cfg.APIKey),
		contestKeys: cfg.ContestKeys,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		logger:      log.Default(),
		contests:    make(map[int]struct{}),
		byLogin:     make(map[string][]TaskResult),
	}, nil
}

// SetLogger переопределяет логгер (для единообразия с логами генерации).
func (c *EjudgeClient) SetLogger(logger *log.Logger) {
	if logger != nil {
		c.logger = logger
	}
}

// SiteName — имя сайта (для регистрации в реестре и аккаунтов учеников).
func (c *EjudgeClient) SiteName() string { return c.ejudgeID }

func (c *EjudgeClient) SupportsTaskScores() bool { return true }

// MatchTaskURL — это ссылка ejudge именно на этот экземпляр (по хосту).
func (c *EjudgeClient) MatchTaskURL(taskURL string) bool {
	parsed, ok := domain.ParseEjudgeTaskURL(taskURL)
	return ok && parsed.Host == c.host
}

// ObserveTaskURL запоминает контест из ссылки задачи (и на контест, и на задачу),
// чтобы потом скачать его прогоны. Вызывается билдом до сбора статусов учеников.
func (c *EjudgeClient) ObserveTaskURL(taskURL string) {
	parsed, ok := domain.ParseEjudgeTaskURL(taskURL)
	if !ok || parsed.Host != c.host {
		return
	}
	c.mu.Lock()
	c.contests[parsed.ContestID] = struct{}{}
	c.mu.Unlock()
}

// FetchUserResults возвращает задачи ученика (по login) во всех встреченных
// контестах. Первый вызов скачивает прогоны всех контестов (master list-runs),
// раскладывает по login; последующие — из кэша.
func (c *EjudgeClient) FetchUserResults(ctx context.Context, login string) ([]TaskResult, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.fetched {
		if err := c.fetchAllLocked(ctx); err != nil {
			return nil, err
		}
		c.fetched = true
	}
	return cloneTaskResults(c.byLogin[login]), nil
}

func (c *EjudgeClient) contestIDsLocked() []int {
	ids := make([]int, 0, len(c.contests))
	for id := range c.contests {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

func (c *EjudgeClient) fetchAllLocked(ctx context.Context) error {
	type aggKey struct {
		login   string
		taskURL string
	}
	type agg struct {
		attempted      bool
		solved         bool
		okSolved       bool
		acceptedSolved bool
		score          int
		hasScore       bool
		timed          []TimedSubmission
	}
	byKey := make(map[aggKey]*agg)

	for _, contestID := range c.contestIDsLocked() {
		runs, err := c.fetchContestRuns(ctx, contestID)
		if err != nil {
			// Один недоступный/битый контест не должен ронять весь источник:
			// логируем и пропускаем, остальные контесты забираются как обычно.
			c.logger.Printf("WARN ejudge %s: fetch contest %d runs failed: %v", c.ejudgeID, contestID, err)
			continue
		}
		for _, run := range runs {
			// Логин сопоставляем без учёта регистра (как handle у codeforces):
			// иначе опечатка в регистре у аккаунта ученика молча теряет его посылки.
			login := strings.ToLower(strings.TrimSpace(run.UserLogin))
			if login == "" || run.ProbID <= 0 {
				continue
			}
			// Незавершённые прогоны (в очереди/на тестировании) не учитываем —
			// полный перезабор на следующей генерации пересчитает их корректно.
			if isEjudgePendingStatus(run.Status) {
				continue
			}
			taskURL := c.problemURL(contestID, run.ProbID)
			key := aggKey{login: login, taskURL: taskURL}
			a := byKey[key]
			if a == nil {
				a = &agg{}
				byKey[key] = a
			}
			a.attempted = true
			solved := isEjudgeSolvedStatus(run.Status)
			border := isEjudgeBorderStatus(run.Status)
			if solved {
				a.solved = true
				if border {
					a.acceptedSolved = true
				}
				// У ejudge ничего не «перебивает» OK, поэтому okSolved не ставим:
				// рамка показывается всегда, когда есть OK (см. isEjudgeBorderStatus).
			}
			score := domain.ClampScore(run.Score)
			if !a.hasScore || score > a.score {
				a.score = score
				a.hasScore = true
			}
			if run.RunTime > 0 {
				submissionScore := score
				a.timed = append(a.timed, TimedSubmission{
					At:       time.Unix(run.RunTime, 0).UTC(),
					Solved:   solved,
					Score:    &submissionScore,
					Accepted: border,
				})
			}
		}
	}

	byLogin := make(map[string][]TaskResult)
	for key, a := range byKey {
		result := TaskResult{TaskURL: key.taskURL, Attempted: a.attempted, Solved: a.solved, Accepted: a.acceptedSolved && !a.okSolved}
		if a.hasScore {
			score := a.score
			result.Score = &score
		}
		if len(a.timed) > 0 {
			sort.SliceStable(a.timed, func(i, j int) bool { return a.timed[i].At.Before(a.timed[j].At) })
			result.Timed = a.timed
		}
		byLogin[key.login] = append(byLogin[key.login], result)
	}
	for login := range byLogin {
		sort.SliceStable(byLogin[login], func(i, j int) bool {
			return byLogin[login][i].TaskURL < byLogin[login][j].TaskURL
		})
	}
	c.byLogin = byLogin
	return nil
}

// problemURL — канонический вид ссылки на задачу (совпадает с normalized_url,
// который строит domain для ссылок из контеста).
func (c *EjudgeClient) problemURL(contestID, probID int) string {
	return fmt.Sprintf("%s/new-client?contest_id=%d&prob_id=%d", c.baseURL, contestID, probID)
}

func (c *EjudgeClient) contestKeyFor(contestID int) string {
	if key, ok := c.contestKeys[strconv.Itoa(contestID)]; ok && strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	return c.apiKey
}

// FetchContestProblems разворачивает контест ejudge в упорядоченный список задач
// (client/contest-status-json). Результат кэшируется на время жизни клиента —
// один и тот же контест из разных групп не перезапрашивается.
func (c *EjudgeClient) FetchContestProblems(ctx context.Context, contestID int) ([]EjudgeProblem, error) {
	c.problemsMu.Lock()
	if c.problemsCache == nil {
		c.problemsCache = make(map[int][]EjudgeProblem)
	}
	if cached, ok := c.problemsCache[contestID]; ok {
		c.problemsMu.Unlock()
		return cached, nil
	}
	c.problemsMu.Unlock()

	q := url.Values{}
	q.Set("contest_id", strconv.Itoa(contestID))

	var result struct {
		Problems []struct {
			ID        int    `json:"id"`
			ShortName string `json:"short_name"`
			LongName  string `json:"long_name"`
		} `json:"problems"`
	}
	if err := c.callAPI(ctx, "client/contest-status-json", q, contestID, &result); err != nil {
		return nil, err
	}
	out := make([]EjudgeProblem, 0, len(result.Problems))
	for _, p := range result.Problems {
		if p.ID <= 0 {
			continue
		}
		out = append(out, EjudgeProblem{ProbID: p.ID, ShortName: strings.TrimSpace(p.ShortName), LongName: strings.TrimSpace(p.LongName)})
	}

	c.problemsMu.Lock()
	c.problemsCache[contestID] = out
	c.problemsMu.Unlock()
	return out, nil
}

type ejudgeRun struct {
	RunID     int    `json:"run_id"`
	RunTime   int64  `json:"run_time"`
	ProbID    int    `json:"prob_id"`
	Status    int    `json:"status"`
	Score     int    `json:"score"`
	UserLogin string `json:"user_login"`
}

// ejudgeListRunsPageSize — размер окна прогонов при постраничном заборе.
const ejudgeListRunsPageSize = 5000

// ejudgeRunsFieldMask — максимальная положительная маска полей list-runs-json:
// запрашивает все доступные поля прогона (в т.ч. user_login и prob_id). Без неё
// часть ejudge отдаёт лишь user_name/prob_name, и прогоны не привязываются к
// ученикам. Отрицательное значение (-1) ejudge отвергает как невалидное.
const ejudgeRunsFieldMask = "9223372036854775807"

// fetchContestRuns скачивает все прогоны контеста через master list-runs-json,
// постранично по run_id. Дедупликация по run_id делает забор устойчивым к
// нюансам семантики first_run/last_run у конкретного ejudge.
func (c *EjudgeClient) fetchContestRuns(ctx context.Context, contestID int) ([]ejudgeRun, error) {
	seen := make(map[int]struct{})
	out := make([]ejudgeRun, 0)
	lo := 0
	for iter := 0; iter < 1000; iter++ {
		q := url.Values{}
		q.Set("contest_id", strconv.Itoa(contestID))
		q.Set("first_run", strconv.Itoa(lo))
		q.Set("last_run", strconv.Itoa(lo+ejudgeListRunsPageSize-1))
		// Полная маска полей: без неё некоторые ejudge отдают только user_name/
		// prob_name (без user_login и prob_id), и все прогоны молча теряются при
		// сопоставлении по логину. Максимальный положительный int64 запрашивает
		// все поля, включая user_login, prob_id, status, score, run_time.
		q.Set("field_mask", ejudgeRunsFieldMask)

		var result struct {
			Runs      []ejudgeRun `json:"runs"`
			TotalRuns int         `json:"total_runs"`
		}
		if err := c.callAPI(ctx, "master/list-runs-json", q, contestID, &result); err != nil {
			return nil, err
		}

		newCount := 0
		maxRun := lo
		for _, run := range result.Runs {
			if _, dup := seen[run.RunID]; dup {
				continue
			}
			seen[run.RunID] = struct{}{}
			out = append(out, run)
			newCount++
			if run.RunID > maxRun {
				maxRun = run.RunID
			}
		}

		if result.TotalRuns > 0 && len(out) >= result.TotalRuns {
			break
		}
		if newCount == 0 {
			break
		}
		lo = maxRun + 1
	}
	return out, nil
}

// callAPI выполняет GET к API ejudge и разбирает конверт {ok,error,result}.
func (c *EjudgeClient) callAPI(ctx context.Context, method string, q url.Values, contestID int, resultOut any) error {
	endpoint := c.apiBase + "/" + method
	if enc := q.Encode(); enc != "" {
		endpoint += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if key := c.contestKeyFor(contestID); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("ejudge %s status=%d body=%q", method, res.StatusCode, strings.TrimSpace(string(body[:min(len(body), 256)])))
	}

	var envelope struct {
		OK    bool `json:"ok"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("ejudge %s: decode envelope: %w", method, err)
	}
	if !envelope.OK {
		msg := "api error"
		if envelope.Error != nil && strings.TrimSpace(envelope.Error.Message) != "" {
			msg = envelope.Error.Message
		}
		return fmt.Errorf("ejudge %s: %s", method, msg)
	}
	if resultOut == nil || len(envelope.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, resultOut); err != nil {
		return fmt.Errorf("ejudge %s: decode result: %w", method, err)
	}
	return nil
}

// Коды вердиктов у ejudge те же, что у informatics (это тот же движок), но
// трактовка рамки другая. На ejudge (kod-u):
//   - OK (0) — «зачтено» преподавателем → зелёный плюс + жёлтая рамка;
//   - «ожидает подтверждения» (RUN_PENDING_REVIEW=16) и «зачтено на проверку»
//     (RUN_ACCEPTED=8) — прошло тесты → зелёный плюс без рамки.
//
// Незавершённые (11 в очереди, 96..99 выполняется/компилируется) — не учитываем.
func isEjudgeSolvedStatus(status int) bool {
	switch status {
	case informaticsStatusOK, informaticsStatusAccepted, informaticsStatusPendingReview:
		return true
	}
	return false
}

// isEjudgeBorderStatus — жёлтая рамка у ejudge только на полном OK (0). Ничего
// его не «перебивает», поэтому suppress-статуса у ejudge нет.
func isEjudgeBorderStatus(status int) bool { return status == informaticsStatusOK }

func isEjudgePendingStatus(status int) bool { return isInformaticsPendingStatus(status) }
