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
	// fioStatusUnknown — ФИО нет в базе, а права заводить новых учеников нет
	// (режим «только из уже заведённых»): строка пропускается.
	fioStatusUnknown = "unknown"
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
	// В админке новых учеников заводить можно всегда.
	plan, status, msg := h.registerGroupNames(strings.TrimSpace(req.Slug), req.Names, apply, true)
	if msg != "" {
		writeJSON(w, status, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "plan": plan, "applied": apply})
}

// registerGroupNames — разбор и (при apply) применение списка ФИО к составу
// группы. allowCreate=false — режим «только из уже заведённых»: незнакомые ФИО
// не создаются, а помечаются отдельным статусом и пропускаются (право
// members.manage распоряжается составом, но не заводит учеников в общей базе).
// Общий helper админки и панели группы.
func (h *Handlers) registerGroupNames(slug, rawNames string, apply, allowCreate bool) (FIORegPlan, int, string) {
	if !domain.IsValidSlug(slug) {
		return FIORegPlan{}, http.StatusBadRequest, "invalid slug"
	}
	groupFile, ok, err := h.readGroupFile(slug)
	if err != nil || !ok {
		return FIORegPlan{}, http.StatusBadRequest, "group not found"
	}
	if len(groupFile.MemberGroups) > 0 {
		return FIORegPlan{}, http.StatusBadRequest, "у объединённой группы нет своего состава — регистрируйте ФИО в группы-участницы"
	}
	students, err := h.loadStudentsList()
	if err != nil {
		return FIORegPlan{}, http.StatusInternalServerError, err.Error()
	}

	memberIDs := make(map[string]struct{}, len(groupFile.StudentIDs))
	for _, id := range domain.NormalizeGroups(groupFile.StudentIDs) {
		memberIDs[id] = struct{}{}
	}

	plan := planFIORegistration(students, memberIDs, rawNames)
	if plan.Total == 0 {
		return FIORegPlan{}, http.StatusBadRequest, "список пуст — введите ФИО по одному в строке"
	}
	if !allowCreate {
		plan.demoteCreateRows()
	}
	if !apply {
		return plan, http.StatusOK, ""
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
			return FIORegPlan{}, http.StatusInternalServerError, err.Error()
		}
	}
	if len(additions) > 0 {
		groupFile.StudentIDs = domain.MergeGroups(groupFile.StudentIDs, additions)
		if err := h.writeGroupFile(slug, groupFile); err != nil {
			return FIORegPlan{}, http.StatusInternalServerError, err.Error()
		}
	}

	return plan, http.StatusOK, ""
}

// demoteCreateRows переводит строки «создать нового» в «пропущено»: в режиме без
// права заводить учеников такие ФИО не создаются. Счётчики пересобираются,
// чтобы превью показывало честные числа.
func (p *FIORegPlan) demoteCreateRows() {
	if p.Create == 0 {
		return
	}
	for i := range p.Rows {
		if p.Rows[i].Status != fioStatusCreate {
			continue
		}
		p.Rows[i].Status = fioStatusUnknown
		p.Rows[i].Note = "такого ученика нет в базе — нужно право «Заводить новых учеников»"
		p.Warnings++
	}
	p.Create = 0
}

// AdminGroupRegisterNamesDryRun — превью регистрации списка ФИО (без записи).
func (h *Handlers) AdminGroupRegisterNamesDryRun(w http.ResponseWriter, r *http.Request) {
	h.adminGroupRegisterNames(w, r, false)
}

// AdminGroupRegisterNamesApply — регистрация списка ФИО на группу.
func (h *Handlers) AdminGroupRegisterNamesApply(w http.ResponseWriter, r *http.Request) {
	h.adminGroupRegisterNames(w, r, true)
}
