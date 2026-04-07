package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultInformaticsBaseURL = "https://informatics.msk.ru"

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
}

func NewInformaticsAPIClientFromFile(path string) (*InformaticsAPIClient, error) {
	creds, err := LoadInformaticsCredentials(path)
	if err != nil {
		return nil, err
	}
	return NewInformaticsAPIClient(creds)
}

func NewInformaticsAPIClient(creds InformaticsCredentials) (*InformaticsAPIClient, error) {
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
	}, nil
}

func (c *InformaticsAPIClient) FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, nil
	}
	if _, err := strconv.Atoi(accountID); err != nil {
		return nil, nil, fmt.Errorf("informatics account_id must be numeric: %w", err)
	}

	if err := c.ensureLoggedIn(ctx, false); err != nil {
		return nil, nil, err
	}

	solvedSet := make(map[string]struct{})
	attemptedSet := make(map[string]struct{})

	page := 1
	for {
		resp, err := c.fetchRunsPage(ctx, accountID, page)
		if errors.Is(err, errInformaticsNotAuthorized) {
			if loginErr := c.ensureLoggedIn(ctx, true); loginErr != nil {
				return nil, nil, loginErr
			}
			resp, err = c.fetchRunsPage(ctx, accountID, page)
		}
		if err != nil {
			return nil, nil, err
		}

		for _, run := range resp.Data {
			if run.Problem.ID <= 0 {
				continue
			}
			taskURL := c.buildTaskURL(run.Problem.ID)
			attemptedSet[taskURL] = struct{}{}
			if run.EjudgeStatus == 0 {
				solvedSet[taskURL] = struct{}{}
			}
		}

		if resp.Metadata.PageCount <= page || len(resp.Data) == 0 {
			break
		}
		page++
	}

	return setKeysSorted(solvedSet), setKeysSorted(attemptedSet), nil
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
	if strings.Contains(bodyText, "loginerrors") || strings.Contains(bodyText, "Вы не вошли в систему") {
		return errors.New("informatics login failed: bad credentials or blocked account")
	}
	if !strings.Contains(bodyText, "logout.php") && !strings.Contains(bodyText, "Вы зашли под именем") {
		return errors.New("informatics login failed: cannot confirm authorized session")
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
	q.Set("count", "100")
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
	return fmt.Sprintf("%s/mod/statements/view.php?chapterid=%d#1", c.baseURL, problemID)
}

func setKeysSorted(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
	EjudgeStatus int                `json:"ejudge_status"`
	Problem      informaticsProblem `json:"problem"`
}

type informaticsProblem struct {
	ID int `json:"id"`
}
