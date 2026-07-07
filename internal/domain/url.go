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

	if canonical, ok := canonicalACMPTaskURL(u); ok {
		return canonical
	}

	if canonical, ok := canonicalInformaticsTaskURL(u); ok {
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

// canonicalACMPTaskURL приводит разные формы ссылок на задачу acmp.ru к единому
// виду по числовому id_task, чтобы ссылка из контеста совпадала с URL, которые
// строит клиент из решённых задач (он отдаёт "https://acmp.ru/?main=task&id_task=N").
// Отличаются обычно путь (/index.asp против /), порядок query, регистр main,
// схема (http/https) и префикс www — всё это к матчингу отношения не имеет.
// Канонизация касается только normalized_url; видимая ссылка остаётся исходной.
func canonicalACMPTaskURL(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "acmp.ru" && host != "www.acmp.ru" {
		return "", false
	}

	rawID := strings.TrimSpace(u.Query().Get("id_task"))
	if rawID == "" {
		return "", false
	}
	id, err := strconv.Atoi(rawID)
	if err != nil || id <= 0 {
		return "", false
	}

	return fmt.Sprintf("https://acmp.ru/?main=task&id_task=%d", id), true
}

// canonicalInformaticsTaskURL приводит ссылки на задачу informatics к единому
// виду по chapterid. Задача на informatics адресуется chapterid (это же id
// задачи в посылках), а параметр id — это сборник/страница-контейнер, который у
// одной и той же задачи может отличаться (та же задача попадает в разные
// сборники/курсы). Ссылка из «браузерной» формы …view.php?id=2296&chapterid=2937
// иначе не совпала бы с посылками, которые строятся как …view.php?chapterid=2937.
// Ссылки-сборники (id без chapterid) не трогаем — это не задача, а страница
// контеста (её разворачивает билд в отдельные задачи).
func canonicalInformaticsTaskURL(u *url.URL) (string, bool) {
	host := strings.ToLower(u.Hostname())
	if host != "informatics.msk.ru" {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(u.Path), "/mod/statements/view.php") {
		return "", false
	}

	rawID := strings.TrimSpace(u.Query().Get("chapterid"))
	if rawID == "" {
		return "", false
	}
	id, err := strconv.Atoi(rawID)
	if err != nil || id <= 0 {
		return "", false
	}

	return fmt.Sprintf("https://informatics.msk.ru/mod/statements/view.php?chapterid=%d", id), true
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
