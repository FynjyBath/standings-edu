package domain

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func NormalizeTaskURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	u, err := url.Parse(s)
	if err != nil {
		return s
	}

	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""

	// Зеркала informatics (informatics.mccme.ru — старый домен того же сайта)
	// канонизируем в informatics.msk.ru, иначе ссылки из контеста не совпадут
	// с URL, которые строятся из посылок, и плюсы не подсветятся. Канонизация
	// касается только сопоставления (normalized_url) — видимая ссылка в таблице
	// остаётся такой, какой её ввели.
	switch u.Host {
	case "informatics.mccme.ru", "www.informatics.mccme.ru", "www.informatics.msk.ru":
		u.Host = "informatics.msk.ru"
	}

	if canonical, ok := canonicalCodeforcesTaskURL(u); ok {
		return canonical
	}

	if u.Path != "/" {
		u.Path = strings.TrimRight(u.Path, "/")
		if u.Path == "" {
			u.Path = "/"
		}
	}

	return u.String()
}

// canonicalCodeforcesTaskURL приводит разные формы ссылок на задачу Codeforces
// к единому виду, чтобы ссылки из data/contests.json совпадали с URL, которые
// строятся из сабмитов пользователя.
//
// Все три варианта одной задачи:
//   - https://codeforces.com/contest/<id>/problem/<idx>
//   - https://codeforces.com/problemset/problem/<id>/<idx>
//   - https://codeforces.com/gym/<id>/problem/<idx>
//
// канонизируются по числовому id: gym (id >= 100000) -> /gym/...,
// остальное -> /problemset/problem/...; индекс задачи приводится к верхнему
// регистру, query/fragment отбрасываются.
func canonicalCodeforcesTaskURL(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "codeforces.com" && host != "www.codeforces.com" {
		return "", false
	}

	segments := splitPathSegments(u.Path)
	if len(segments) < 4 {
		return "", false
	}

	var rawID, rawIndex string
	switch {
	case strings.EqualFold(segments[0], "contest") && strings.EqualFold(segments[2], "problem"):
		rawID, rawIndex = segments[1], segments[3]
	case strings.EqualFold(segments[0], "gym") && strings.EqualFold(segments[2], "problem"):
		rawID, rawIndex = segments[1], segments[3]
	case strings.EqualFold(segments[0], "problemset") && strings.EqualFold(segments[1], "problem"):
		rawID, rawIndex = segments[2], segments[3]
	default:
		return "", false
	}

	id, err := strconv.Atoi(rawID)
	if err != nil || id <= 0 {
		return "", false
	}

	index := strings.ToUpper(strings.TrimSpace(rawIndex))
	if index == "" {
		return "", false
	}

	if id >= 100000 {
		return fmt.Sprintf("https://codeforces.com/gym/%d/problem/%s", id, index), true
	}
	return fmt.Sprintf("https://codeforces.com/problemset/problem/%d/%s", id, index), true
}

func splitPathSegments(path string) []string {
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
