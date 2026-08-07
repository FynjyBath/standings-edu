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
	// Access — права доступа; TokenValid — доступ что-то даёт сверх публичного;
	// Token — токен для ссылок; JuryKonduits — контест является кондуитом.
	Access       *GroupAccess
	TokenValid   bool
	Token        string
	JuryKonduits map[string]bool
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
	h.renderContestPage(w, r, slug, h.resolveAccess(slug, r))
}

func (h *Handlers) renderContestPage(w http.ResponseWriter, r *http.Request, slug string, acc *GroupAccess) {
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

	unfrozen := h.applyAccessView(&standings, slug, acc)

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
		Access:       acc,
		TokenValid:   acc.Elevated(),
		UnfrozenView: unfrozen,
		Token:        acc.Token,
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
	// Кнопка «заполнить кондуит» — при праве на заполнение.
	if acc.Has(domain.PermKonduitFill) {
		var extras GroupPageData
		h.juryStandingsExtras(slug, &extras, acc)
		page.JuryKonduits = extras.JuryKonduits
	}
	page.BackURL = "/standings/" + slug + h.tokenQuery(acc.Token)
	page.BackLabel = "← Таблицы группы"

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
