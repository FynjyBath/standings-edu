package httpapi

// Анкеты своей группы — только чтение (право intake.view). Учитель видит, кто
// подал заявку и с какими аккаунтами, и может заметить опечатку до того, как
// анкету подтвердят. Подтверждение и правка остаются за админкой: merge пишет в
// общий students.json и может слить анкету с учеником другой группы.

import (
	"net/http"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

// GroupIntakePageData — страница анкет группы.
type GroupIntakePageData struct {
	PageTitle  string
	Footer     FooterInfo
	GroupSlug  string
	GroupTitle string
	// Token — токен доступа для ссылок со страницы (пусто — вошли по сессии).
	Token string
	// RoleTitle — подпись доступа в шапке.
	RoleTitle string
	Rows      []GroupIntakeRow
}

// GroupIntakeRow — одна анкета в списке.
type GroupIntakeRow struct {
	FullName   string
	PublicName string
	Accounts   []domain.Account
	// InGroup — ученик с таким ФИО уже есть в группе: анкету, скорее всего, уже
	// подтвердили (или это повторная отправка).
	InGroup bool
	// KnownStudent — ученик с таким ФИО уже есть в общей базе (в другой группе
	// или ещё не добавлен в эту).
	KnownStudent bool
	// OtherGroups — другие группы, указанные в анкете (обычно пусто).
	OtherGroups []string
}

// GroupIntakePage — список анкет, поданных в эту группу.
func (h *Handlers) GroupIntakePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("group_name")
	acc, ok := h.signInToGroup(w, r, slug)
	if !ok {
		return
	}
	if !acc.Has(domain.PermIntakeView) {
		http.Error(w, "нет права смотреть анкеты группы", http.StatusForbidden)
		return
	}
	gf, found := h.readSourceGroupFile(slug)
	if !found {
		http.NotFound(w, r)
		return
	}
	if h.intake == nil {
		http.Error(w, "приём анкет не настроен", http.StatusInternalServerError)
		return
	}

	pending, err := h.intake.PendingIntake(h.dataPath("student_intake_admin.json"))
	if err != nil {
		h.logger.Printf("ERROR read intake for group %s: %v", slug, err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Кто уже есть в базе и кто уже в этой группе — чтобы отличить свежую
	// анкету от уже обработанной.
	inGroup := make(map[string]struct{})
	for _, id := range h.resolveGroupStudentIDs(slug) {
		inGroup[id] = struct{}{}
	}
	knownByName := make(map[string]string) // ФИО → id ученика в общей базе
	if students, err := h.loadStudentsList(); err == nil {
		for _, st := range students {
			knownByName[strings.TrimSpace(st.FullName)] = st.ID
		}
	}

	title := strings.TrimSpace(gf.Title)
	if title == "" {
		title = slug
	}
	page := GroupIntakePageData{
		PageTitle:  title + " — анкеты",
		Footer:     h.buildFooterInfo(),
		GroupSlug:  slug,
		GroupTitle: title,
		Token:      acc.Token,
		RoleTitle:  acc.Title(),
	}
	for _, st := range pending {
		if !containsSlug(st.Groups, slug) {
			continue // анкета не в эту группу — не наше дело
		}
		row := GroupIntakeRow{
			FullName:   st.FullName,
			PublicName: st.PublicName,
			Accounts:   st.Accounts,
		}
		if id, known := knownByName[strings.TrimSpace(st.FullName)]; known {
			row.KnownStudent = true
			if _, member := inGroup[id]; member {
				row.InGroup = true
			}
		}
		for _, g := range st.Groups {
			if !strings.EqualFold(strings.TrimSpace(g), slug) {
				row.OtherGroups = append(row.OtherGroups, strings.TrimSpace(g))
			}
		}
		page.Rows = append(page.Rows, row)
	}
	sort.SliceStable(page.Rows, func(i, j int) bool {
		return strings.ToLower(page.Rows[i].FullName) < strings.ToLower(page.Rows[j].FullName)
	})

	w.Header().Set("Cache-Control", "no-store")
	if err := h.renderer.Render(w, http.StatusOK, "group_intake.html", page); err != nil {
		h.logger.Printf("ERROR render group intake slug=%s: %v", slug, err)
	}
}

// containsSlug — есть ли слаг в списке (без учёта регистра и пробелов).
func containsSlug(list []string, slug string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), slug) {
			return true
		}
	}
	return false
}
