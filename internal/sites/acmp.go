package sites

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultACMPBaseURL = "https://acmp.ru"

var (
	acmpBlockRe  = regexp.MustCompile(`(?is)<b\s+class=btext>.*?</b>\s*<p\s+class=text>(.*?)</p>`)
	acmpTaskIDRe = regexp.MustCompile(`id_task=(\d+)`)
)

type ACMPClient struct {
	baseURL    string
	httpClient *http.Client
}

func NewACMPClient() *ACMPClient {
	return &ACMPClient{
		baseURL: defaultACMPBaseURL,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *ACMPClient) FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, nil
	}
	if _, err := strconv.Atoi(accountID); err != nil {
		return nil, nil, fmt.Errorf("acmp account_id must be numeric: %w", err)
	}

	pageURL := fmt.Sprintf("%s/index.asp?main=user&id=%s", c.baseURL, accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, nil, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, nil, fmt.Errorf("acmp user page status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, nil, err
	}

	solvedSet, attemptedSet, err := parseACMPUserPage(body)
	if err != nil {
		return nil, nil, err
	}

	return mapSetToSortedURLs(c.baseURL, solvedSet), mapSetToSortedURLs(c.baseURL, attemptedSet), nil
}

func parseACMPUserPage(body []byte) (map[int]struct{}, map[int]struct{}, error) {
	matches := acmpBlockRe.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("acmp: no task blocks found")
	}

	taskBlocks := make([][]byte, 0, 4)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		if !acmpTaskIDRe.Match(m[1]) {
			continue
		}
		taskBlocks = append(taskBlocks, m[1])
	}

	if len(taskBlocks) < 2 {
		return nil, nil, fmt.Errorf("acmp: solved/unsolved blocks not found")
	}

	solved := extractTaskIDs(taskBlocks[0])
	attemptedOnly := extractTaskIDs(taskBlocks[1])

	// Ensure precedence solved > attempted.
	for taskID := range solved {
		delete(attemptedOnly, taskID)
	}

	return solved, attemptedOnly, nil
}

func extractTaskIDs(block []byte) map[int]struct{} {
	out := make(map[int]struct{})
	all := acmpTaskIDRe.FindAllSubmatch(block, -1)
	for _, m := range all {
		if len(m) < 2 {
			continue
		}
		taskID, err := strconv.Atoi(string(m[1]))
		if err != nil || taskID <= 0 {
			continue
		}
		out[taskID] = struct{}{}
	}
	return out
}

func mapSetToSortedURLs(baseURL string, set map[int]struct{}) []string {
	ids := make([]int, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, fmt.Sprintf("%s/?main=task&id_task=%d", baseURL, id))
	}
	return out
}
