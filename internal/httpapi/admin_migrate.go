package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/migrate"
)

type AdminExportPageData struct {
	PageTitle string
	Footer    FooterInfo
	// Groups — активные группы; Archived — архивные, отдельным свёрнутым
	// блоком (их со временем становится больше, чем активных).
	Groups   []AdminGroupLink
	Archived []AdminGroupLink
}

// slugSetFromForm превращает список слагов из формы в множество валидных slug→true.
func slugSetFromForm(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if domain.IsValidSlug(v) {
			set[v] = true
		}
	}
	return set
}

type AdminImportPageData struct {
	PageTitle string
	Footer    FooterInfo
}

// AdminExportPage — страница выбора групп для экспорта в бандл.
func (h *Handlers) AdminExportPage(w http.ResponseWriter, _ *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	links, err := h.listAdminGroupLinks()
	if err != nil {
		h.logger.Printf("ERROR admin export list groups: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	page := AdminExportPageData{
		PageTitle: "Экспорт данных",
		Footer:    h.buildFooterInfo(),
	}
	for _, link := range links {
		if link.Archived {
			page.Archived = append(page.Archived, link)
		} else {
			page.Groups = append(page.Groups, link)
		}
	}
	if err := h.renderer.Render(w, http.StatusOK, "admin_export.html", page); err != nil {
		h.logger.Printf("ERROR render admin export: %v", err)
	}
}

// AdminExportDownload собирает бандл выбранных групп и отдаёт его файлом.
func (h *Handlers) AdminExportDownload(w http.ResponseWriter, r *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	sel := migrate.Selection{
		Participants: slugSetFromForm(r.Form["participants"]),
		Contests:     slugSetFromForm(r.Form["contests"]),
	}
	includeTokens := r.FormValue("include_tokens") != ""

	bundle, err := migrate.BuildBundle(h.admin.cfg.DataDir, sel, includeTokens)
	if err != nil {
		h.logger.Printf("ERROR export build bundle: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	filename := "standings-export-" + time.Now().Format("2006-01-02") + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(bundle); err != nil {
		h.logger.Printf("ERROR export encode: %v", err)
	}
}

// AdminImportPage — страница загрузки бандла.
func (h *Handlers) AdminImportPage(w http.ResponseWriter, _ *http.Request) {
	if h.admin == nil {
		http.Error(w, "admin is not configured", http.StatusInternalServerError)
		return
	}
	page := AdminImportPageData{PageTitle: "Импорт данных", Footer: h.buildFooterInfo()}
	if err := h.renderer.Render(w, http.StatusOK, "admin_import.html", page); err != nil {
		h.logger.Printf("ERROR render admin import: %v", err)
	}
}

// AdminImportApply принимает файл-бандл и дописывает его в data-директорию.
func (h *Handlers) AdminImportApply(w http.ResponseWriter, r *http.Request) {
	result := h.runAdminAction("import", func() AdminActionResult {
		return h.executeImportAction(r)
	})
	h.setAdminResult(result)
	http.Redirect(w, r, "/standings/admin", http.StatusSeeOther)
}

func (h *Handlers) executeImportAction(r *http.Request) AdminActionResult {
	started := time.Now()

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return newAdminResult("import", false, -1, started, "", []string{"не удалось прочитать форму: " + err.Error()})
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		return newAdminResult("import", false, -1, started, "", []string{"файл бандла не выбран"})
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, 128<<20))
	if err != nil {
		return newAdminResult("import", false, -1, started, "", []string{"не удалось прочитать файл: " + err.Error()})
	}

	// Ссылки informatics из чужого бандла приводим к нашему зеркалу сразу —
	// проще всего по всему тексту: покрывает и задачи, и материалы, и конфиги.
	body = []byte(h.rewriteInformaticsText(string(body)))

	var bundle migrate.Bundle
	if err := json.Unmarshal(body, &bundle); err != nil {
		return newAdminResult("import", false, -1, started, "", []string{"некорректный JSON бандла: " + err.Error()})
	}
	// Принимаем текущую и более старые версии (импорт умеет читать легаси-формат
	// с таблицами кондуитов внутри provider_config). Новее — отклоняем.
	if bundle.Version < 1 || bundle.Version > migrate.BundleVersion {
		return newAdminResult("import", false, -1, started, "",
			[]string{fmt.Sprintf("неподдерживаемая версия бандла: %d (поддерживаются 1..%d)", bundle.Version, migrate.BundleVersion)})
	}

	// Выбор участников/контестов по группам приходит из формы (клиент строит
	// чекбоксы, прочитав файл). Если формой ничего не задано (has_selection не
	// выставлен) — импортируем всё.
	var sel migrate.Selection
	if r.FormValue("has_selection") == "1" {
		sel = migrate.Selection{
			Participants: slugSetFromForm(r.Form["import_participants"]),
			Contests:     slugSetFromForm(r.Form["import_contests"]),
		}
	}

	rep, err := migrate.ImportBundle(h.admin.cfg.DataDir, &bundle, sel)
	if err != nil {
		return newAdminResult("import", false, -1, started, "", []string{err.Error()})
	}

	var out bytes.Buffer
	fmt.Fprintf(&out, "Импорт завершён.\n")
	fmt.Fprintf(&out, "Ученики: добавлено %d, обновлено %d.\n", rep.StudentsAdded, rep.StudentsUpdated)
	fmt.Fprintf(&out, "Глобальные контесты: добавлено %d.\n", rep.ContestsAdded)
	for _, g := range rep.Groups {
		state := "обновлена"
		if g.Created {
			state = "создана"
		}
		fmt.Fprintf(&out, "Группа %s (%s): +%d учеников, +%d контестов", g.Slug, state, g.StudentsAdded, g.ContestsAdded)
		if g.MembersAdded > 0 {
			fmt.Fprintf(&out, ", +%d групп в объединении", g.MembersAdded)
		}
		if g.GradesAdded > 0 {
			fmt.Fprintf(&out, ", +%d ручных оценок", g.GradesAdded)
		}
		if g.FlagReviewsAdded > 0 {
			fmt.Fprintf(&out, ", +%d проверок флагов", g.FlagReviewsAdded)
		}
		out.WriteString("\n")
	}
	for _, wn := range rep.Warnings {
		fmt.Fprintf(&out, "⚠ %s\n", wn)
	}
	out.WriteString("\nНе забудьте «Сгенерировать».")

	return newAdminResult("import", true, 0, started, out.String(), nil)
}
