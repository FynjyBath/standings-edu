package sites

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func (c *CodeforcesAPIClient) FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error) {
	handle := strings.TrimSpace(accountID)
	if handle == "" {
		return nil, nil, nil
	}

	solvedSet := make(map[string]struct{})
	attemptedSet := make(map[string]struct{})

	const (
		maxPages = 30
	)
	from := 1

	for page := 0; page < maxPages; page++ {
		resp, err := c.fetchPage(ctx, handle, from, codeforcesPageSize)
		if err != nil {
			return nil, nil, err
		}

		for _, submission := range resp.Result {
			taskURL := buildCodeforcesProblemURL(submission.Problem)
			if taskURL == "" {
				continue
			}
			attemptedSet[taskURL] = struct{}{}
			if submission.Verdict == "OK" {
				solvedSet[taskURL] = struct{}{}
			}
		}

		if len(resp.Result) < codeforcesPageSize {
			break
		}
		from += codeforcesPageSize
	}

	solved = mapKeysSorted(solvedSet)
	attempted = mapKeysSorted(attemptedSet)
	return solved, attempted, nil
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

func mapKeysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

type codeforcesAPIResponse struct {
	Status  string                 `json:"status"`
	Comment string                 `json:"comment"`
	Result  []codeforcesSubmission `json:"result"`
}

type codeforcesSubmission struct {
	Verdict string            `json:"verdict"`
	Problem codeforcesProblem `json:"problem"`
}

type codeforcesProblem struct {
	ContestID int    `json:"contestId"`
	Index     string `json:"index"`
}
