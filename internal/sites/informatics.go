package sites

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

func NewInformaticsAPIClientFromFile(path string) (*InformaticsAPIClient, error) {
	return NewInformaticsAPIClientFromFileWithState(path, "")
}

func NewInformaticsAPIClientFromFileWithState(path string, statePath string) (*InformaticsAPIClient, error) {
	creds, err := LoadInformaticsCredentials(path)
	if err != nil {
		return nil, err
	}
	return NewInformaticsAPIClientWithState(creds, statePath)
}

func NewInformaticsAPIClient(creds InformaticsCredentials) (*InformaticsAPIClient, error) {
	return NewInformaticsAPIClientWithState(creds, "")
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
	maxRunID := lastKnownRunID

	firstPage, err := c.fetchRunsPageWithRelogin(ctx, accountID, 1)
	if err != nil {
		return nil, err
	}
	stopOnKnownRunID := lastKnownRunID > 0
	staleReached := mergeRunsIntoAggregatesSinceRunID(firstPage.Data, lastKnownRunID, stopOnKnownRunID, aggByTask, c.buildTaskURL, &maxRunID)

	pageCount := firstPage.Metadata.PageCount
	if pageCount > 1 && len(firstPage.Data) > 0 && !staleReached {
		if stopOnKnownRunID {
			for page := 2; page <= pageCount; page++ {
				resp, pageErr := c.fetchRunsPageWithRelogin(ctx, accountID, page)
				if pageErr != nil {
					return nil, pageErr
				}
				if mergeRunsIntoAggregatesSinceRunID(resp.Data, lastKnownRunID, true, aggByTask, c.buildTaskURL, &maxRunID) {
					break
				}
			}
		} else {
			otherPages, pageErr := c.fetchRemainingRunsPages(ctx, accountID, pageCount)
			if pageErr != nil {
				return nil, pageErr
			}
			for _, page := range otherPages {
				mergeRunsIntoAggregatesSinceRunID(page.Data, 0, false, aggByTask, c.buildTaskURL, &maxRunID)
			}
		}
	}

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
	if host != "informatics.msk.ru" && host != "www.informatics.msk.ru" {
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

func mergeRunsIntoAggregatesSinceRunID(runs []informaticsRun, lastKnownRunID int, stopOnKnownRunID bool, aggByTask map[string]informaticsTaskAggregate, buildTaskURL func(problemID int) string, maxRunID *int) (staleReached bool) {
	for _, run := range runs {
		if run.ID <= 0 {
			continue
		}

		if run.ID > *maxRunID {
			*maxRunID = run.ID
		}

		if lastKnownRunID > 0 && run.ID <= lastKnownRunID {
			if stopOnKnownRunID {
				return true
			}
			continue
		}

		taskURL := buildTaskURL(run.Problem.ID)
		if taskURL == "" {
			continue
		}

		agg := aggByTask[taskURL]
		agg.attempted = true
		if run.EjudgeStatus == 0 {
			agg.solved = true
		}

		score := inferInformaticsScore(run)
		if !agg.hasScore || score > agg.score {
			agg.score = score
			agg.hasScore = true
		}

		aggByTask[taskURL] = agg
	}
	return false
}

func inferInformaticsScore(run informaticsRun) int {
	if run.EjudgeScore.Valid {
		return clampInformaticsScore(run.EjudgeScore.Value)
	}
	if run.Score.Valid {
		return clampInformaticsScore(run.Score.Value)
	}
	if run.EjudgeStatus == 0 {
		return 100
	}
	return 0
}

func clampInformaticsScore(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
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
		}

		if result.Score != nil {
			score := clampInformaticsScore(*result.Score)
			if !agg.hasScore || score > agg.score {
				agg.score = score
				agg.hasScore = true
			}
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
			value := clampInformaticsScore(agg.score)
			score = &value
		}

		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: agg.attempted,
			Solved:    agg.solved,
			Score:     score,
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
	c.stateLoaded = true

	if c.statePath == "" {
		return nil
	}

	b, err := os.ReadFile(c.statePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read informatics state %q: %w", c.statePath, err)
	}

	var decoded informaticsStateFile
	if err := json.Unmarshal(b, &decoded); err != nil {
		return fmt.Errorf("decode informatics state %q: %w", c.statePath, err)
	}
	if decoded.Accounts == nil {
		decoded.Accounts = make(map[string]informaticsAccountState)
	}

	c.accountState = decoded.Accounts
	return nil
}

func (c *InformaticsAPIClient) persistStateLocked() error {
	if c.statePath == "" {
		return nil
	}

	state := informaticsStateFile{Accounts: c.accountState}
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

	res, err := c.httpClient.Do(req)
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
	score     int
	hasScore  bool
}

type informaticsStateFile struct {
	Accounts map[string]informaticsAccountState `json:"accounts"`
}

type informaticsAccountState struct {
	MaxRunID  int          `json:"max_run_id"`
	Results   []TaskResult `json:"results,omitempty"`
	Solved    []string     `json:"solved,omitempty"`
	Attempted []string     `json:"attempted,omitempty"`
	UpdatedAt time.Time    `json:"updated_at"`
}
