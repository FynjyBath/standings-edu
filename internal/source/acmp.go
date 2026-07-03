package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const defaultACMPBaseURL = "https://acmp.ru"

var (
	// Захватываем заголовок блока (группа 1) и список задач (группа 2), чтобы
	// классифицировать блок по названию, а не по позиции.
	acmpBlockRe  = regexp.MustCompile(`(?is)<b\s+class=btext>([^<]*)</b>\s*<p\s+class=text>(.*?)</p>`)
	acmpTaskIDRe = regexp.MustCompile(`id_task=(\d+)`)

	// Маркеры заголовков в кодировке windows-1251 (страница acmp в cp1251).
	// "Нерешен" = Н е р е ш е н; "Решен" = Р е ш е н (с заглавной Р).
	acmpUnsolvedMarker = []byte{0xCD, 0xE5, 0xF0, 0xE5, 0xF8, 0xE5, 0xED}
	acmpSolvedMarker   = []byte{0xD0, 0xE5, 0xF8, 0xE5, 0xED}
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

func (c *ACMPClient) FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error) {
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil
	}
	if _, err := strconv.Atoi(accountID); err != nil {
		return nil, fmt.Errorf("acmp account_id must be numeric: %w", err)
	}

	pageURL := fmt.Sprintf("%s/index.asp?main=user&id=%s", c.baseURL, accountID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, err
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		return nil, fmt.Errorf("acmp user page status=%d body=%q", res.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	solvedSet, attemptedSet, err := parseACMPUserPage(body)
	if err != nil {
		return nil, err
	}

	out := make([]TaskResult, 0, len(solvedSet)+len(attemptedSet))
	for _, taskURL := range mapSetToSortedURLs(c.baseURL, solvedSet) {
		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: true,
			Solved:    true,
		})
	}
	for _, taskURL := range mapSetToSortedURLs(c.baseURL, attemptedSet) {
		out = append(out, TaskResult{
			TaskURL:   taskURL,
			Attempted: true,
			Solved:    false,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].TaskURL < out[j].TaskURL
	})
	return out, nil
}

func (c *ACMPClient) SupportsTaskScores() bool {
	return false
}

func (c *ACMPClient) MatchTaskURL(taskURL string) bool {
	u, err := url.Parse(strings.TrimSpace(taskURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "acmp.ru" || host == "www.acmp.ru"
}

func parseACMPUserPage(body []byte) (map[int]struct{}, map[int]struct{}, error) {
	matches := acmpBlockRe.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil, nil, fmt.Errorf("acmp: no task blocks found")
	}

	solved := make(map[int]struct{})
	attemptedOnly := make(map[int]struct{})
	found := false

	// На странице может быть несколько списков «Решенные задачи» (например, общий
	// и по разделу «Курсы»). Классифицируем каждый блок по заголовку и объединяем
	// все решённые (и все нерешённые), а не берём только первый список.
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		header := m[1]
		tasksBlock := m[2]
		if !acmpTaskIDRe.Match(tasksBlock) {
			continue
		}

		switch {
		case bytes.Contains(header, acmpUnsolvedMarker):
			for taskID := range extractTaskIDs(tasksBlock) {
				attemptedOnly[taskID] = struct{}{}
			}
			found = true
		case bytes.Contains(header, acmpSolvedMarker):
			for taskID := range extractTaskIDs(tasksBlock) {
				solved[taskID] = struct{}{}
			}
			found = true
		}
	}

	if !found {
		return nil, nil, fmt.Errorf("acmp: solved/unsolved blocks not found")
	}

	// Решено имеет приоритет над попытками.
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
