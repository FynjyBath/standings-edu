package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"standings-edu/internal/domain"
	"standings-edu/internal/source"
	"standings-edu/internal/studentintake"
)

// Регистрация списка ФИО на группу: вставляется список ФИО (по одному в строке).
// Для неизвестных ФИО создаётся пустая запись ученика (без аккаунтов) и он
// добавляется в группу; известные — просто дописываются в группу. Есть dry-run.

// Статусы строки превью.
const (
	fioStatusCreate    = "create"    // нового ученика создать + в группу
	fioStatusAdd       = "add"       // существующего дописать в группу
	fioStatusAlready   = "already"   // уже в группе — ничего не делаем
	fioStatusAmbiguous = "ambiguous" // несколько учеников с таким ФИО — пропуск
	fioStatusDuplicate = "duplicate" // повтор в самом списке — пропуск
)

// maxFIORegLines — верхний предел строк, чтобы случайная огромная вставка не
// подвесила обработку (тело запроса и так ограничено maxAdminJSONBodyBytes).
const maxFIORegLines = 5000

// FIORegRow — одна строка результата (превью или применения).
type FIORegRow struct {
	Input     string `json:"input"`
	FullName  string `json:"full_name"`
	Status    string `json:"status"`
	StudentID string `json:"student_id,omitempty"`
	Note      string `json:"note"`
}

// FIORegPlan — итог разбора списка: строки + счётчики.
type FIORegPlan struct {
	Rows     []FIORegRow `json:"rows"`
	Create   int         `json:"create"`
	Add      int         `json:"add"`
	Already  int         `json:"already"`
	Warnings int         `json:"warnings"` // ambiguous + duplicate
	Total    int         `json:"total"`    // распознано непустых ФИО
	Overflow bool        `json:"overflow"` // список обрезан по лимиту строк
}

// planFIORegistration разбирает список ФИО относительно существующих учеников и
// состава группы. Ничего не пишет. memberIDs — уже состоящие в группе id.
// Матчинг по ФИО без учёта регистра/пробелов/ё (source.NormalizeName); при
// нескольких учениках с одинаковым ФИО строка помечается неоднозначной и
// пропускается (чтобы не приписать не тому).
func planFIORegistration(students []domain.Student, memberIDs map[string]struct{}, rawNames string) FIORegPlan {
	byKey := make(map[string][]int)
	for i, s := range students {
		fn := domain.NormalizeWhitespace(s.FullName)
		if fn == "" {
			continue
		}
		k := source.NormalizeName(fn)
		byKey[k] = append(byKey[k], i)
	}

	plan := FIORegPlan{Rows: make([]FIORegRow, 0)}
	seen := make(map[string]struct{})
	lines := strings.Split(strings.ReplaceAll(rawNames, "\r\n", "\n"), "\n")
	for _, line := range lines {
		full := domain.NormalizeWhitespace(line)
		if full == "" {
			continue
		}
		if plan.Total >= maxFIORegLines {
			plan.Overflow = true
			break
		}
		plan.Total++

		row := FIORegRow{Input: strings.TrimSpace(line), FullName: full}
		key := source.NormalizeName(full)
		if _, dup := seen[key]; dup {
			row.Status, row.Note = fioStatusDuplicate, "повторяется в списке — пропущено"
			plan.Warnings++
			plan.Rows = append(plan.Rows, row)
			continue
		}
		seen[key] = struct{}{}

		idxs := byKey[key]
		switch {
		case len(idxs) == 0:
			row.Status, row.Note = fioStatusCreate, "новый ученик (пустая запись) + в группу"
			plan.Create++
		case len(idxs) == 1:
			sid := students[idxs[0]].ID
			row.StudentID = sid
			if _, ok := memberIDs[sid]; ok {
				row.Status, row.Note = fioStatusAlready, "уже в группе"
				plan.Already++
			} else {
				row.Status, row.Note = fioStatusAdd, "добавить в группу"
				plan.Add++
			}
		default:
			row.Status = fioStatusAmbiguous
			row.Note = fmt.Sprintf("несколько учеников (%d) с таким ФИО — пропущено, добавьте вручную", len(idxs))
			plan.Warnings++
		}
		plan.Rows = append(plan.Rows, row)
	}
	return plan
}

// AdminGroupRegisterNames — dry-run/apply регистрации списка ФИО на группу.
// apply=false: только превью; apply=true: создаёт новых учеников и дописывает
// группу существующим/новым.
func (h *Handlers) adminGroupRegisterNames(w http.ResponseWriter, r *http.Request, apply bool) {
	if h.admin == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "admin is not configured"})
		return
	}
	var req struct {
		Slug  string `json:"slug"`
		Names string `json:"names"`
	}
	if err := decodeAdminJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request body"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if !domain.IsValidSlug(slug) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid slug"})
		return
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "group not found"})
		return
	}
	if len(groupFile.MemberGroups) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "у объединённой группы нет своего состава — регистрируйте ФИО в группы-участницы"})
		return
	}
	students, err := h.loadStudentsList()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	memberIDs := make(map[string]struct{}, len(groupFile.StudentIDs))
	for _, id := range domain.NormalizeGroups(groupFile.StudentIDs) {
		memberIDs[id] = struct{}{}
	}

	plan := planFIORegistration(students, memberIDs, req.Names)
	if plan.Total == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "список пуст — введите ФИО по одному в строке"})
		return
	}

	if !apply {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": false})
		return
	}

	// Применение: создаём новых учеников (уникальные id, пустые аккаунты) и
	// собираем id, кому дописать группу.
	taken := make(map[string]struct{}, len(students))
	for _, s := range students {
		if id := strings.TrimSpace(s.ID); id != "" {
			taken[id] = struct{}{}
		}
	}
	isTaken := func(id string) bool { _, ok := taken[id]; return ok }

	additions := make([]string, 0, plan.Add+plan.Create)
	created := 0
	for i := range plan.Rows {
		switch plan.Rows[i].Status {
		case fioStatusAdd:
			additions = append(additions, plan.Rows[i].StudentID)
		case fioStatusCreate:
			id := studentintake.GenerateUniqueID(plan.Rows[i].FullName, isTaken)
			taken[id] = struct{}{}
			students = append(students, domain.Student{
				ID:         id,
				FullName:   plan.Rows[i].FullName,
				PublicName: studentintake.GeneratePublicNameFromFullName(plan.Rows[i].FullName),
			})
			plan.Rows[i].StudentID = id
			additions = append(additions, id)
			created++
		}
	}

	if created > 0 {
		if err := studentintake.WriteStudentsFile(h.dataPath("students.json"), students); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}
	if len(additions) > 0 {
		groupFile.StudentIDs = domain.MergeGroups(groupFile.StudentIDs, additions)
		if err := h.writeGroupFile(slug, groupFile); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": true})
}

// AdminGroupRegisterNamesDryRun — превью регистрации списка ФИО (без записи).
func (h *Handlers) AdminGroupRegisterNamesDryRun(w http.ResponseWriter, r *http.Request) {
	h.adminGroupRegisterNames(w, r, false)
}

// AdminGroupRegisterNamesApply — регистрация списка ФИО на группу.
func (h *Handlers) AdminGroupRegisterNamesApply(w http.ResponseWriter, r *http.Request) {
	h.adminGroupRegisterNames(w, r, true)
}
