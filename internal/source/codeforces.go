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
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"standings-edu/internal/domain"
)

const defaultCodeforcesBaseURL = "https://codeforces.com/api"
const codeforcesPageSize = 10000
const codeforcesContestStatusPageSize = 1000
const codeforcesAPIRandBytes = 3

type CodeforcesAPIClient struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	apiSecret  string
	minGap     time.Duration
	rateMu     sync.Mutex
	lastReqAt  time.Time

	statePath    string
	stateMu      sync.Mutex
	stateLoaded  bool
	accountState map[string]codeforcesAccountState
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

type codeforcesAPIRequestError struct {
	MethodName string
	HTTPStatus int
	APIComment string
	Err        error
}

func (e *codeforcesAPIRequestError) Error() string {
	if e == nil {
		return ""
	}

	switch {
	case e.HTTPStatus > 0 && e.Err != nil:
		return fmt.Sprintf("codeforces %s status=%d: %v", e.MethodName, e.HTTPStatus, e.Err)
	case e.HTTPStatus > 0:
		return fmt.Sprintf("codeforces %s status=%d", e.MethodName, e.HTTPStatus)
	case strings.TrimSpace(e.APIComment) != "":
		return fmt.Sprintf("codeforces %s api error: %s", e.MethodName, e.APIComment)
	case e.Err != nil:
		return fmt.Sprintf("codeforces %s: %v", e.MethodName, e.Err)
	default:
		return fmt.Sprintf("codeforces %s request failed", e.MethodName)
	}
}

func (e *codeforcesAPIRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewCodeforcesAPIClient() *CodeforcesAPIClient {
	return NewCodeforcesAPIClientWithState("")
}

func NewCodeforcesAPIClientWithState(statePath string) *CodeforcesAPIClient {
	return &CodeforcesAPIClient{
		baseURL: defaultCodeforcesBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		minGap:       350 * time.Millisecond,
		statePath:    strings.TrimSpace(statePath),
		accountState: make(map[string]codeforcesAccountState),
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
	return NewCodeforcesAPIClientFromFileWithState(path, "")
}

func NewCodeforcesAPIClientFromFileWithState(path string, statePath string) (*CodeforcesAPIClient, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return NewCodeforcesAPIClientWithState(statePath), nil
	}

	creds, err := LoadCodeforcesCredentials(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewCodeforcesAPIClientWithState(statePath), nil
		}
		return nil, err
	}
	return NewCodeforcesAPIClientWithCredentialsAndState(creds, statePath)
}

func NewCodeforcesAPIClientWithCredentials(creds CodeforcesCredentials) (*CodeforcesAPIClient, error) {
	return NewCodeforcesAPIClientWithCredentialsAndState(creds, "")
}

func NewCodeforcesAPIClientWithCredentialsAndState(creds CodeforcesCredentials, statePath string) (*CodeforcesAPIClient, error) {
	key := strings.TrimSpace(creds.Key)
	secret := strings.TrimSpace(creds.Secret)
	if (key == "") != (secret == "") {
		return nil, errors.New("codeforces credentials require both key and secret")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(creds.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultCodeforcesBaseURL
	}

	client := NewCodeforcesAPIClientWithState(statePath)
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
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			Err:        err,
		}
	}

	if err := c.waitRateLimit(ctx); err != nil {
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			Err:        err,
		}
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			Err:        err,
		}
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			HTTPStatus: res.StatusCode,
			Err:        fmt.Errorf("body=%q", strings.TrimSpace(string(body))),
		}
	}

	var decoded codeforcesContestStandingsAPIResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			Err:        err,
		}
	}
	if decoded.Status != "OK" {
		return CodeforcesContestStandings{}, &codeforcesAPIRequestError{
			MethodName: "contest.standings",
			APIComment: decoded.Comment,
		}
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

func (c *CodeforcesAPIClient) FetchContestStatusSubmissions(ctx context.Context, contestID int, handles []string, showUnofficial bool) ([]codeforcesContestStatusSubmission, error) {
	if contestID <= 0 {
		return nil, fmt.Errorf("invalid contest_id=%d", contestID)
	}

	normalizedHandles := normalizeCodeforcesHandles(handles)
	if len(normalizedHandles) == 0 {
		return nil, fmt.Errorf("empty handles list")
	}

	targetHandleSet := make(map[string]struct{}, len(normalizedHandles))
	for _, handle := range normalizedHandles {
		targetHandleSet[strings.ToLower(handle)] = struct{}{}
	}

	out := make([]codeforcesContestStatusSubmission, 0, codeforcesContestStatusPageSize)
	seenSubmissionIDs := make(map[int]struct{}, codeforcesContestStatusPageSize)

	for _, handle := range normalizedHandles {
		from := 1
		for {
			page, err := c.fetchContestStatusPage(ctx, contestID, handle, from, codeforcesContestStatusPageSize)
			if err != nil {
				return nil, err
			}

			for _, submission := range page.Result {
				if !showUnofficial && !isCodeforcesStatusOfficialParticipant(submission.Author.ParticipantType) {
					continue
				}
				if !codeforcesSubmissionHasAnyTargetHandle(submission, targetHandleSet) {
					continue
				}
				if _, exists := seenSubmissionIDs[submission.ID]; exists {
					continue
				}
				seenSubmissionIDs[submission.ID] = struct{}{}
				out = append(out, submission)
			}

			if len(page.Result) < codeforcesContestStatusPageSize {
				break
			}
			from += codeforcesContestStatusPageSize
		}
	}

	return out, nil
}

func (c *CodeforcesAPIClient) FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error) {
	handle := strings.TrimSpace(accountID)
	if handle == "" {
		return nil, nil
	}

	state, hasState, err := c.getAccountState(handle)
	if err != nil {
		return nil, err
	}

	aggByTask := make(map[string]codeforcesTaskAggregate)
	if hasState {
		mergeCodeforcesStateIntoAggregates(aggByTask, state.Results)
	}

	lastKnownSubmissionID := 0
	if hasState {
		lastKnownSubmissionID = state.MaxSubmissionID
	}
	maxSubmissionID := lastKnownSubmissionID
	hasNewSubmissions := false

	const maxPages = 30
	from := 1

	for page := 0; page < maxPages; page++ {
		resp, err := c.fetchPage(ctx, handle, from, codeforcesPageSize)
		if err != nil {
			return nil, err
		}

		if mergeCodeforcesSubmissionsIntoAggregatesSinceID(resp.Result, lastKnownSubmissionID, lastKnownSubmissionID > 0, aggByTask, &maxSubmissionID, &hasNewSubmissions) {
			break
		}

		if len(resp.Result) < codeforcesPageSize {
			break
		}
		from += codeforcesPageSize
	}

	if hasState && !hasNewSubmissions {
		return cloneTaskResults(state.Results), nil
	}

	results := codeforcesAggregatesToTaskResults(aggByTask)

	if maxSubmissionID > lastKnownSubmissionID || !hasState {
		newState := codeforcesAccountState{
			MaxSubmissionID: maxSubmissionID,
			Results:         results,
			UpdatedAt:       time.Now().UTC(),
		}
		if saveErr := c.saveAccountState(handle, newState); saveErr != nil {
			return nil, saveErr
		}
	}

	return results, nil
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

func (c *CodeforcesAPIClient) fetchContestStatusPage(ctx context.Context, contestID int, handle string, from int, count int) (codeforcesContestStatusAPIResponse, error) {
	q := make(url.Values)
	q.Set("contestId", strconv.Itoa(contestID))
	q.Set("from", strconv.Itoa(from))
	q.Set("count", strconv.Itoa(count))
	if strings.TrimSpace(handle) != "" {
		q.Set("handle", handle)
	}

	req, err := c.newAPIRequest(ctx, "contest.status", q)
	if err != nil {
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			Err:        err,
		}
	}

	if err := c.waitRateLimit(ctx); err != nil {
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			Err:        err,
		}
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			Err:        err,
		}
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			HTTPStatus: res.StatusCode,
			Err:        fmt.Errorf("body=%q", strings.TrimSpace(string(body))),
		}
	}

	var decoded codeforcesContestStatusAPIResponse
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			Err:        err,
		}
	}
	if decoded.Status != "OK" {
		return codeforcesContestStatusAPIResponse{}, &codeforcesAPIRequestError{
			MethodName: "contest.status",
			APIComment: decoded.Comment,
		}
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
		b.WriteString(p.key)
		b.WriteByte('=')
		b.WriteString(p.value)
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

func isCodeforcesStatusOfficialParticipant(participantType string) bool {
	typ := strings.ToLower(strings.TrimSpace(participantType))
	return typ == "" || typ == "contestant"
}

func codeforcesSubmissionHasAnyTargetHandle(submission codeforcesContestStatusSubmission, targetHandleSet map[string]struct{}) bool {
	for _, member := range submission.Author.Members {
		key := strings.ToLower(strings.TrimSpace(member.Handle))
		if key == "" {
			continue
		}
		if _, ok := targetHandleSet[key]; ok {
			return true
		}
	}
	return false
}

func mergeCodeforcesSubmissionsIntoAggregatesSinceID(submissions []codeforcesSubmission, lastKnownSubmissionID int, stopOnKnownSubmissionID bool, aggByTask map[string]codeforcesTaskAggregate, maxSubmissionID *int, hasNewSubmissions *bool) (staleReached bool) {
	for _, submission := range submissions {
		if submission.ID > *maxSubmissionID {
			*maxSubmissionID = submission.ID
		}

		if lastKnownSubmissionID > 0 && submission.ID > 0 && submission.ID <= lastKnownSubmissionID {
			if stopOnKnownSubmissionID {
				return true
			}
			continue
		}

		if submission.ID > lastKnownSubmissionID {
			*hasNewSubmissions = true
		}

		taskURL := buildCodeforcesProblemURL(submission.Problem)
		if taskURL == "" {
			continue
		}

		agg := aggByTask[taskURL]
		agg.attempted = true
		if submission.Verdict == "OK" {
			agg.solved = true
		}
		score := codeforcesSubmissionScore(submission)
		if !agg.hasScore || score > agg.score {
			agg.score = score
			agg.hasScore = true
		}
		aggByTask[taskURL] = agg
	}
	return false
}

func mergeCodeforcesStateIntoAggregates(aggByTask map[string]codeforcesTaskAggregate, results []TaskResult) {
	for _, result := range results {
		taskURL := strings.TrimSpace(result.TaskURL)
		if taskURL == "" {
			continue
		}

		agg := aggByTask[taskURL]
		attempted := result.Attempted || result.Solved || result.Score != nil
		if attempted {
			agg.attempted = true
		}
		if result.Solved {
			agg.solved = true
		}
		if result.Score != nil {
			score := domain.ClampScore(*result.Score)
			if !agg.hasScore || score > agg.score {
				agg.score = score
				agg.hasScore = true
			}
		}
		aggByTask[taskURL] = agg
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

func codeforcesAggregatesToTaskResults(aggByTask map[string]codeforcesTaskAggregate) []TaskResult {
	out := make([]TaskResult, 0, len(aggByTask))
	for taskURL, agg := range aggByTask {
		if !agg.attempted && !agg.solved {
			continue
		}

		if !agg.hasScore {
			if agg.solved {
				agg.score = 100
			} else {
				agg.score = 0
			}
		}
		score := domain.ClampScore(agg.score)
		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: agg.attempted,
			Solved:    agg.solved,
			Score:     &score,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].TaskURL < out[j].TaskURL
	})
	return out
}

func cloneTaskResults(results []TaskResult) []TaskResult {
	if len(results) == 0 {
		return nil
	}

	cloned := make([]TaskResult, 0, len(results))
	for _, result := range results {
		copyResult := result
		if result.Score != nil {
			score := *result.Score
			copyResult.Score = &score
		}
		cloned = append(cloned, copyResult)
	}
	return cloned
}

func (c *CodeforcesAPIClient) getAccountState(accountID string) (codeforcesAccountState, bool, error) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if err := c.loadStateLocked(); err != nil {
		return codeforcesAccountState{}, false, err
	}

	state, ok := c.accountState[accountID]
	return state, ok, nil
}

func (c *CodeforcesAPIClient) saveAccountState(accountID string, state codeforcesAccountState) error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()

	if err := c.loadStateLocked(); err != nil {
		return err
	}

	c.accountState[accountID] = state
	return c.persistStateLocked()
}

func (c *CodeforcesAPIClient) loadStateLocked() error {
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
		return fmt.Errorf("read codeforces state %q: %w", c.statePath, err)
	}

	var decoded codeforcesStateFile
	if err := json.Unmarshal(b, &decoded); err != nil {
		return fmt.Errorf("decode codeforces state %q: %w", c.statePath, err)
	}
	if decoded.Accounts == nil {
		decoded.Accounts = make(map[string]codeforcesAccountState)
	}

	c.accountState = decoded.Accounts
	c.stateLoaded = true
	return nil
}

func (c *CodeforcesAPIClient) persistStateLocked() error {
	if c.statePath == "" {
		return nil
	}

	state := codeforcesStateFile{Accounts: c.accountState}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal codeforces state: %w", err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(filepath.Dir(c.statePath), 0o755); err != nil {
		return fmt.Errorf("mkdir codeforces state dir: %w", err)
	}

	tmpFile, err := os.CreateTemp(filepath.Dir(c.statePath), "codeforces-state-*.tmp")
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

func isCodeforcesRetriableError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var requestErr *codeforcesAPIRequestError
	if errors.As(err, &requestErr) {
		if requestErr.HTTPStatus == http.StatusTooManyRequests || requestErr.HTTPStatus >= 500 {
			return true
		}
		if isTemporaryCodeforcesAPIComment(requestErr.APIComment) {
			return true
		}
		if requestErr.Err != nil {
			return isCodeforcesRetriableError(requestErr.Err)
		}
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
		type temporary interface {
			Temporary() bool
		}
		if t, ok := any(netErr).(temporary); ok && t.Temporary() {
			return true
		}
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	return false
}

func isTemporaryCodeforcesAPIComment(comment string) bool {
	text := strings.ToLower(strings.TrimSpace(comment))
	if text == "" {
		return false
	}

	needles := []string{
		"temporary",
		"temporarily",
		"try again",
		"retry",
		"server error",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"timeout",
		"timed out",
		"upstream",
		"limit exceeded",
		"rate limit",
		"too many requests",
		"internal error",
	}
	for _, needle := range needles {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
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
	ID      int               `json:"id"`
	Verdict string            `json:"verdict"`
	Points  *float64          `json:"points"`
	Problem codeforcesProblem `json:"problem"`
}

type codeforcesTaskAggregate struct {
	attempted bool
	solved    bool
	score     int
	hasScore  bool
}

type codeforcesStateFile struct {
	Accounts map[string]codeforcesAccountState `json:"accounts"`
}

type codeforcesAccountState struct {
	MaxSubmissionID int          `json:"max_submission_id"`
	Results         []TaskResult `json:"results,omitempty"`
	UpdatedAt       time.Time    `json:"updated_at"`
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

type codeforcesContestStatusAPIResponse struct {
	Status  string                              `json:"status"`
	Comment string                              `json:"comment"`
	Result  []codeforcesContestStatusSubmission `json:"result"`
}

type codeforcesContestStatusSubmission struct {
	ID                  int                            `json:"id"`
	Verdict             string                         `json:"verdict"`
	Points              *float64                       `json:"points"`
	RelativeTimeSeconds int                            `json:"relativeTimeSeconds"`
	Problem             codeforcesContestStatusProblem `json:"problem"`
	Author              codeforcesContestStatusAuthor  `json:"author"`
}

type codeforcesContestStatusProblem struct {
	ContestID int      `json:"contestId"`
	Index     string   `json:"index"`
	Name      string   `json:"name"`
	Points    *float64 `json:"points"`
}

type codeforcesContestStatusAuthor struct {
	ParticipantType string                    `json:"participantType"`
	Members         []codeforcesContestMember `json:"members"`
}
