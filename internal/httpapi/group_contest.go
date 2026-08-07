package httpapi

// Страница одного контеста: /standings/<slug>/contest?id=<contestID> (и
// панельный вариант /standings/<slug>/panel/contest?id=…).
//
// Зачем отдельная страница: из сводной раньше вели якоря вида …#contest-<id> на
// общую страницу группы, но её высота меняется уже после прыжка (ленивая
// подгрузка соседних таблиц + свёртка длинных), поэтому попадание «мазало».
// Здесь показывается ровно одна таблица — прыгать некуда, страница лёгкая, а
// ссылку на конкретный контест можно переслать.

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/storage"
)

// GroupContestPageData — данные страницы одного контеста.
type GroupContestPageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	Contest    domain.GeneratedContestStandings
	// TokenValid/Token — доступ не ниже наблюдателя (полные таблицы, скрытое) и
	// токен для ссылок; JuryKonduits — контест является кондуитом (роль жюри).
	TokenValid   bool
	Token        string
	JuryKonduits map[string]bool
	// InPanel/RoleTitle — страница открыта из панели группы.
	InPanel   bool
	RoleTitle string
	// GroupArchived — группа в архиве: под таблицей показываем «обновлено».
	GroupArchived bool
	// UnfrozenView — показана полная версия замороженной таблицы.
	UnfrozenView bool
	// Prev/Next — соседние контесты группы (для «листания»); пусто — края.
	PrevID, PrevTitle string
	NextID, NextTitle string
	// BackURL/BackLabel — возврат: в панель или к таблицам группы.
	BackURL   string
	BackLabel string
}

// GroupContestPage — страница одного контеста по обычной ссылке (публично или
// по токену наблюдателя).
func (h *Handlers) GroupContestPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	role := h.groupRole(slug, token, "")
	h.renderContestPage(w, r, slug, role, token, false)
}

// GroupPanelContestPage — та же страница из панели: вход по логину и паролю,
// ссылки ведут обратно в панель.
func (h *Handlers) GroupPanelContestPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	role, _, ok := h.authorizePanelRequest(w, r, slug)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	h.renderContestPage(w, r, slug, role, h.groupTokenOf(slug), true)
}

func (h *Handlers) renderContestPage(w http.ResponseWriter, r *http.Request, slug string, role GroupRole, token string, inPanel bool) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.NotFound(w, r)
		return
	}
	standings, err := h.loadGroupStandings(slug)
	if err != nil {
		if errors.Is(err, storage.ErrInvalidGroupSlug) || errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		h.logger.Printf("ERROR contest page slug=%s id=%s err=%v", slug, id, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	elevated := role.AtLeast(RoleObserver)
	unfrozen := h.applyFreezeViewForRole(&standings, slug, elevated)
	if elevated {
		domain.SwapEjudgeLinksToJudge(&standings)
	}

	idx := -1
	for i := range standings.Contests {
		if standings.Contests[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Контеста нет или он скрыт от этой роли — как будто страницы нет.
		http.NotFound(w, r)
		return
	}
	contest := standings.Contests[idx]

	title := strings.TrimSpace(contest.Title)
	if title == "" {
		title = contest.ID
	}
	page := GroupContestPageData{
		PageTitle:    title + " — " + standings.GroupTitle,
		Footer:       h.buildFooterInfo(),
		GroupSlug:    slug,
		GroupTitle:   standings.GroupTitle,
		Contest:      contest,
		TokenValid:   elevated,
		UnfrozenView: unfrozen,
		InPanel:      inPanel,
		RoleTitle:    role.Title(),
	}
	if elevated {
		page.Token = token
	}
	if idx > 0 {
		prev := standings.Contests[idx-1]
		page.PrevID, page.PrevTitle = prev.ID, contestPageTitle(prev)
	}
	if idx+1 < len(standings.Contests) {
		next := standings.Contests[idx+1]
		page.NextID, page.NextTitle = next.ID, contestPageTitle(next)
	}
	if gf, ok := h.readSourceGroupFile(slug); ok {
		page.GroupArchived = gf.Archived()
	}
	// Кнопка «заполнить кондуит» — только роли жюри и выше.
	if role.AtLeast(RoleJury) {
		var extras GroupPageData
		h.juryStandingsExtras(slug, &extras, role)
		page.JuryKonduits = extras.JuryKonduits
	}
	if inPanel {
		page.BackURL = "/standings/" + slug + "/panel"
		page.BackLabel = "← В панель группы"
	} else {
		page.BackURL = "/standings/" + slug + h.tokenQuery(token)
		page.BackLabel = "← Таблицы группы"
	}

	if err := h.renderer.Render(w, http.StatusOK, "group_contest.html", page); err != nil {
		h.logger.Printf("ERROR render contest page slug=%s id=%s err=%v", slug, id, err)
	}
}

// contestPageTitle — подпись контеста для навигации «предыдущий/следующий».
func contestPageTitle(c domain.GeneratedContestStandings) string {
	if t := strings.TrimSpace(c.Title); t != "" {
		return t
	}
	return c.ID
}

// tokenQuery — "?token=…" или пустая строка.
func (h *Handlers) tokenQuery(token string) string {
	if strings.TrimSpace(token) == "" {
		return ""
	}
	return "?token=" + url.QueryEscape(token)
}
