package source

import (
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

const defaultCodeforcesBaseURL = "https://codeforces.com/api"
const codeforcesPageSize = 10000
const codeforcesAPIRandBytes = 3

type CodeforcesAPIClient struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	apiSecret  string
	minGap     time.Duration
	rateMu     sync.Mutex
	lastReqAt  time.Time
}

type CodeforcesCredentials struct {
	Key     string `json:"key"`
	Secret  string `json:"secret"`
	BaseURL string `json:"base_url,omitempty"`
}

type CodeforcesContestStandings struct {
	ContestID   int
	ContestName string
	Problems    []CodeforcesContestProblem
	Rows        []CodeforcesContestRow
}

type CodeforcesContestProblem struct {
	Index  string
	Name   string
	Points *float64
}

type CodeforcesContestRow struct {
	Rank           int
	Penalty        *int
	Handles        []string
	ProblemResults []CodeforcesContestProblemResult
}

type CodeforcesContestProblemResult struct {
	Points               float64
	RejectedAttemptCount int
}

func NewCodeforcesAPIClient() *CodeforcesAPIClient {
	return &CodeforcesAPIClient{
		baseURL: defaultCodeforcesBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		minGap: 350 * time.Millisecond,
	}
}

func LoadCodeforcesCredentials(path string) (CodeforcesCredentials, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return CodeforcesCredentials{}, fmt.Errorf("read codeforces credentials: %w", err)
	}

	if strings.TrimSpace(string(b)) == "" {
		return CodeforcesCredentials{}, nil
	}

	var creds CodeforcesCredentials
	if err := json.Unmarshal(b, &creds); err != nil {
		return CodeforcesCredentials{}, fmt.Errorf("decode codeforces credentials: %w", err)
	}

	creds.Key = strings.TrimSpace(creds.Key)
	creds.Secret = strings.TrimSpace(creds.Secret)
	creds.BaseURL = strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	if creds.BaseURL == "" {
		creds.BaseURL = defaultCodeforcesBaseURL
	}

	if (creds.Key == "") != (creds.Secret == "") {
		return CodeforcesCredentials{}, errors.New("codeforces credentials require both key and secret")
	}

	return creds, nil
}

func NewCodeforcesAPIClientFromFile(path string) (*CodeforcesAPIClient, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return NewCodeforcesAPIClient(), nil
	}

	creds, err := LoadCodeforcesCredentials(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewCodeforcesAPIClient(), nil
		}
		return nil, err
	}
	return NewCodeforcesAPIClientWithCredentials(creds)
}

func NewCodeforcesAPIClientWithCredentials(creds CodeforcesCredentials) (*CodeforcesAPIClient, error) {
	key := strings.TrimSpace(creds.Key)
	secret := strings.TrimSpace(creds.Secret)
	if (key == "") != (secret == "") {
		return nil, errors.New("codeforces credentials require both key and secret")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultCodeforcesBaseURL
	}

	client := NewCodeforcesAPIClient()
	client.baseURL = baseURL
	client.apiKey = key
	client.apiSecret = secret
	return client, nil
}

func (c *CodeforcesAPIClient) FetchContestStandings(ctx context.Context, contestID int, handles []string, showUnofficial bool) (CodeforcesContestStandings, error) {
	if contestID <= 0 {
		return CodeforcesContestStandings{}, fmt.Errorf("invalid contest_id=%d", contestID)
	}

	normalizedHandles := normalizeCodeforcesHandles(handles)
	if len(normalizedHandles) == 0 {
		return CodeforcesContestStandings{}, fmt.Errorf("empty handles list")
	}

	q := make(url.Values)
	q.Set("contestId", strconv.Itoa(contestID))
	q.Set("from", "1")
	q.Set("count", strconv.Itoa(len(normalizedHandles)))
	q.Set("handles", strings.Join(normalizedHandles, ";"))
	if showUnofficial {
		q.Set("showUnofficial", "true")
	} else {
		q.Set("showUnofficial", "false")
	}

	req, err := c.newAPIRequest(ctx, "contest.standings", q)
	if err != nil {
		return CodeforcesContestStandings{}, err
	}

	if err := c.waitRateLimit(ctx); err != nil {
		return CodeforcesContestStandings{}, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return CodeforcesContestStandings{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return CodeforcesContestStandings{}, fmt.Errorf("codeforces api status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded codeforcesContestStandingsAPIResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return CodeforcesContestStandings{}, err
	}
	if decoded.Status != "OK" {
		return CodeforcesContestStandings{}, fmt.Errorf("codeforces api error: %s", decoded.Comment)
	}

	out := CodeforcesContestStandings{
		ContestID:   decoded.Result.Contest.ID,
		ContestName: decoded.Result.Contest.Name,
		Problems:    make([]CodeforcesContestProblem, 0, len(decoded.Result.Problems)),
		Rows:        make([]CodeforcesContestRow, 0, len(decoded.Result.Rows)),
	}

	for _, p := range decoded.Result.Problems {
		out.Problems = append(out.Problems, CodeforcesContestProblem{
			Index:  p.Index,
			Name:   p.Name,
			Points: p.Points,
		})
	}

	for _, row := range decoded.Result.Rows {
		handles := make([]string, 0, len(row.Party.Members))
		for _, member := range row.Party.Members {
			handle := strings.TrimSpace(member.Handle)
			if handle == "" {
				continue
			}
			handles = append(handles, handle)
		}

		results := make([]CodeforcesContestProblemResult, 0, len(row.ProblemResults))
		for _, pr := range row.ProblemResults {
			results = append(results, CodeforcesContestProblemResult{
				Points:               pr.Points,
				RejectedAttemptCount: pr.RejectedAttemptCount,
			})
		}

		out.Rows = append(out.Rows, CodeforcesContestRow{
			Rank:           row.Rank,
			Penalty:        row.Penalty,
			Handles:        handles,
			ProblemResults: results,
		})
	}

	return out, nil
}

func (c *CodeforcesAPIClient) FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error) {
	handle := strings.TrimSpace(accountID)
	if handle == "" {
		return nil, nil
	}

	type aggregate struct {
		attempted bool
		solved    bool
		score     int
		hasScore  bool
	}
	aggByTask := make(map[string]aggregate)

	const maxPages = 30
	from := 1

	for page := 0; page < maxPages; page++ {
		resp, err := c.fetchPage(ctx, handle, from, codeforcesPageSize)
		if err != nil {
			return nil, err
		}

		for _, submission := range resp.Result {
			taskURL := buildCodeforcesProblemURL(submission.Problem)
			if taskURL == "" {
				continue
			}

			a := aggByTask[taskURL]
			a.attempted = true
			if submission.Verdict == "OK" {
				a.solved = true
			}

			score := codeforcesSubmissionScore(submission)
			if !a.hasScore || score > a.score {
				a.score = score
				a.hasScore = true
			}
			aggByTask[taskURL] = a
		}

		if len(resp.Result) < codeforcesPageSize {
			break
		}
		from += codeforcesPageSize
	}

	out := make([]TaskResult, 0, len(aggByTask))
	for taskURL, a := range aggByTask {
		score := a.score
		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: a.attempted,
			Solved:    a.solved,
			Score:     &score,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].TaskURL < out[j].TaskURL
	})
	return out, nil
}

func (c *CodeforcesAPIClient) SupportsTaskScores() bool {
	return true
}

func (c *CodeforcesAPIClient) MatchTaskURL(taskURL string) bool {
	u, err := url.Parse(strings.TrimSpace(taskURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "codeforces.com" && host != "www.codeforces.com" {
		return false
	}
	path := strings.ToLower(strings.TrimSpace(u.Path))
	return strings.HasPrefix(path, "/problemset/problem/") || strings.HasPrefix(path, "/gym/") || strings.Contains(path, "/problem/")
}

func (c *CodeforcesAPIClient) fetchPage(ctx context.Context, handle string, from int, count int) (codeforcesAPIResponse, error) {
	q := make(url.Values)
	q.Set("handle", handle)
	q.Set("from", strconv.Itoa(from))
	q.Set("count", strconv.Itoa(count))

	req, err := c.newAPIRequest(ctx, "user.status", q)
	if err != nil {
		return codeforcesAPIResponse{}, err
	}

	if err := c.waitRateLimit(ctx); err != nil {
		return codeforcesAPIResponse{}, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return codeforcesAPIResponse{}, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return codeforcesAPIResponse{}, fmt.Errorf("codeforces api status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded codeforcesAPIResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return codeforcesAPIResponse{}, err
	}
	if decoded.Status != "OK" {
		return codeforcesAPIResponse{}, fmt.Errorf("codeforces api error: %s", decoded.Comment)
	}

	return decoded, nil
}

func (c *CodeforcesAPIClient) newAPIRequest(ctx context.Context, methodName string, params url.Values) (*http.Request, error) {
	methodName = strings.TrimSpace(strings.TrimLeft(methodName, "/"))
	if methodName == "" {
		return nil, errors.New("codeforces method name is required")
	}

	u, err := url.Parse(c.baseURL + "/" + methodName)
	if err != nil {
		return nil, err
	}

	if params == nil {
		params = make(url.Values)
	}
	if err := c.addSignedQueryParams(methodName, params); err != nil {
		return nil, err
	}

	u.RawQuery = params.Encode()
	return http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
}

func (c *CodeforcesAPIClient) addSignedQueryParams(methodName string, params url.Values) error {
	if c == nil || c.apiKey == "" || c.apiSecret == "" {
		return nil
	}

	params.Set("apiKey", c.apiKey)
	params.Set("time", strconv.FormatInt(time.Now().Unix(), 10))

	randPrefix, err := generateCodeforcesAPIRand()
	if err != nil {
		return fmt.Errorf("generate codeforces api rand: %w", err)
	}

	signatureBase := fmt.Sprintf("%s/%s?%s#%s", randPrefix, methodName, encodeCodeforcesQueryParams(params), c.apiSecret)
	hash := sha512.Sum512([]byte(signatureBase))
	params.Set("apiSig", randPrefix+hex.EncodeToString(hash[:]))
	return nil
}

func encodeCodeforcesQueryParams(params url.Values) string {
	type pair struct {
		key   string
		value string
	}

	pairs := make([]pair, 0, len(params))
	for key, values := range params {
		if key == "apiSig" {
			continue
		}
		for _, value := range values {
			pairs = append(pairs, pair{key: key, value: value})
		}
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].value < pairs[j].value
	})

	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(p.key))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(p.value))
	}
	return b.String()
}

func generateCodeforcesAPIRand() (string, error) {
	buf := make([]byte, codeforcesAPIRandBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func buildCodeforcesProblemURL(p codeforcesProblem) string {
	if p.ContestID == 0 || strings.TrimSpace(p.Index) == "" {
		return ""
	}

	if p.ContestID >= 100000 {
		return fmt.Sprintf("https://codeforces.com/gym/%d/problem/%s", p.ContestID, url.PathEscape(p.Index))
	}
	return fmt.Sprintf("https://codeforces.com/problemset/problem/%d/%s", p.ContestID, url.PathEscape(p.Index))
}

func codeforcesSubmissionScore(sub codeforcesSubmission) int {
	if sub.Points != nil {
		return domain.ClampScore(int(math.Round(*sub.Points)))
	}
	if sub.Verdict == "OK" {
		return 100
	}
	return 0
}

func normalizeCodeforcesHandles(handles []string) []string {
	out := make([]string, 0, len(handles))
	seen := make(map[string]struct{}, len(handles))
	for _, raw := range handles {
		handle := strings.TrimSpace(raw)
		if handle == "" {
			continue
		}
		key := strings.ToLower(handle)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, handle)
	}
	return out
}

func (c *CodeforcesAPIClient) waitRateLimit(ctx context.Context) error {
	if c == nil || c.minGap <= 0 {
		return nil
	}

	for {
		c.rateMu.Lock()
		wait := c.minGap - time.Since(c.lastReqAt)
		if c.lastReqAt.IsZero() || wait <= 0 {
			c.lastReqAt = time.Now()
			c.rateMu.Unlock()
			return nil
		}
		c.rateMu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type codeforcesAPIResponse struct {
	Status  string                 `json:"status"`
	Comment string                 `json:"comment"`
	Result  []codeforcesSubmission `json:"result"`
}

type codeforcesSubmission struct {
	Verdict string            `json:"verdict"`
	Points  *float64          `json:"points"`
	Problem codeforcesProblem `json:"problem"`
}

type codeforcesProblem struct {
	ContestID int    `json:"contestId"`
	Index     string `json:"index"`
}

type codeforcesContestStandingsAPIResponse struct {
	Status  string                              `json:"status"`
	Comment string                              `json:"comment"`
	Result  codeforcesContestStandingsAPIResult `json:"result"`
}

type codeforcesContestStandingsAPIResult struct {
	Contest  codeforcesContestMeta           `json:"contest"`
	Problems []codeforcesContestProblemMeta  `json:"problems"`
	Rows     []codeforcesContestStandingsRow `json:"rows"`
}

type codeforcesContestMeta struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type codeforcesContestProblemMeta struct {
	Index  string   `json:"index"`
	Name   string   `json:"name"`
	Points *float64 `json:"points"`
}

type codeforcesContestStandingsRow struct {
	Rank           int                                  `json:"rank"`
	Penalty        *int                                 `json:"penalty"`
	Party          codeforcesContestParty               `json:"party"`
	ProblemResults []codeforcesContestProblemResultMeta `json:"problemResults"`
}

type codeforcesContestParty struct {
	Members []codeforcesContestMember `json:"members"`
}

type codeforcesContestMember struct {
	Handle string `json:"handle"`
}

type codeforcesContestProblemResultMeta struct {
	Points               float64 `json:"points"`
	RejectedAttemptCount int     `json:"rejectedAttemptCount"`
}
