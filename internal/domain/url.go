package domain

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// IsInformaticsURL — ссылка ведёт на informatics (основной домен или зеркало).
func IsInformaticsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return isInformaticsHost(u.Hostname())
}

// informaticsHosts — все хосты informatics (основной и старое зеркало mccme).
func isInformaticsHost(host string) bool {
	switch strings.ToLower(host) {
	case "informatics.msk.ru", "www.informatics.msk.ru",
		"informatics.mccme.ru", "www.informatics.mccme.ru":
		return true
	}
	return false
}

// RewriteInformaticsHost меняет хост informatics-URL на хост из baseURL (обычно
// base_url кредов), чтобы любые вставленные ссылки (msk/mccme) в итоге вели на
// настроенное зеркало. Не-informatics ссылки, пустой/битый baseURL не трогает.
func RewriteInformaticsHost(rawURL, baseURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return rawURL
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Host == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || !isInformaticsHost(u.Hostname()) {
		return rawURL
	}
	u.Host = base.Host
	return u.String()
}

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

	if canonical, ok := canonicalEjudgeTaskURL(u); ok {
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

// canonicalEjudgeTaskURL приводит ссылки на задачу/контест любого ejudge
// (new-client) к единому виду по contest_id (+ prob_id для конкретной задачи).
// Хост оставляем — он различает экземпляры ejudge; схему приводим к https,
// прочие параметры (SID, action, locale_id…) и фрагмент отбрасываем. Экземпляр
// ejudge узнаётся по форме URL (путь …/new-client + contest_id), а не по
// списку хостов, поэтому работает для любого сконфигурированного ejudge.
// Канонизация касается только сопоставления (normalized_url).
func canonicalEjudgeTaskURL(u *url.URL) (string, bool) {
	if !isEjudgeNewClientPath(u.Path) {
		return "", false
	}
	q := u.Query()
	contestID, err := strconv.Atoi(strings.TrimSpace(q.Get("contest_id")))
	if err != nil || contestID <= 0 {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", false
	}

	base := fmt.Sprintf("https://%s/new-client?contest_id=%d", host, contestID)
	if probID, err := strconv.Atoi(strings.TrimSpace(q.Get("prob_id"))); err == nil && probID > 0 {
		base += fmt.Sprintf("&prob_id=%d", probID)
	}
	return base, true
}

// isEjudgeNewClientPath распознаёт путь клиента ejudge: сам "/new-client" или
// оканчивающийся на него (например "/cgi-bin/new-client").
func isEjudgeNewClientPath(path string) bool {
	path = strings.ToLower(strings.TrimRight(strings.TrimSpace(path), "/"))
	return path == "/new-client" || strings.HasSuffix(path, "/new-client")
}

// EjudgeTaskURL — разобранная ссылка ejudge new-client.
type EjudgeTaskURL struct {
	Host      string
	ContestID int
	ProbID    int // 0 — ссылка на весь контест (разворачивается в отдельные задачи)
}

// ParseEjudgeTaskURL распознаёт ссылку ejudge new-client (по форме URL, без
// привязки к конкретному хосту). Возвращает (…, true), если это ссылка на контест
// (contest_id без prob_id) или на задачу (contest_id + prob_id). host в нижнем
// регистре.
func ParseEjudgeTaskURL(rawURL string) (EjudgeTaskURL, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return EjudgeTaskURL{}, false
	}
	if !isEjudgeNewClientPath(u.Path) {
		return EjudgeTaskURL{}, false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return EjudgeTaskURL{}, false
	}
	q := u.Query()
	contestID, err := strconv.Atoi(strings.TrimSpace(q.Get("contest_id")))
	if err != nil || contestID <= 0 {
		return EjudgeTaskURL{}, false
	}
	out := EjudgeTaskURL{Host: host, ContestID: contestID}
	if probID, err := strconv.Atoi(strings.TrimSpace(q.Get("prob_id"))); err == nil && probID > 0 {
		out.ProbID = probID
	}
	return out, true
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
