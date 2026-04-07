package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultCodeforcesBaseURL = "https://codeforces.com/api"
const codeforcesPageSize = 10000

type CodeforcesAPIClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewCodeforcesAPIClient() *CodeforcesAPIClient {
	return &CodeforcesAPIClient{
		baseURL: defaultCodeforcesBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
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
	u, err := url.Parse(c.baseURL + "/user.status")
	if err != nil {
		return codeforcesAPIResponse{}, err
	}

	q := u.Query()
	q.Set("handle", handle)
	q.Set("from", strconv.Itoa(from))
	q.Set("count", strconv.Itoa(count))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
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
		return clampScore(int(math.Round(*sub.Points)), 0, 100)
	}
	if sub.Verdict == "OK" {
		return 100
	}
	return 0
}

func clampScore(v int, min int, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
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
