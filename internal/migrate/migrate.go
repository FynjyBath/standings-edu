// Package migrate экспортирует/импортирует данные между серверами: группы, их
// учеников и контесты — единым JSON-бандлом. Импорт только ДОПИСЫВАЕТ новое
// (учеников, контесты, состав групп) и никогда не перезаписывает и не удаляет
// существующее, поэтому повторный импорт того же бандла безопасен (no-op).
//
// Для каждой группы можно отдельно выбирать, брать её участников и/или контесты
// (Selection) — и на экспорте, и на импорте. Ученики собираются той же логикой,
// что и merge анкет (studentintake.MergeStudents + AddStudentsToGroups):
// сопоставление по ФИО, доливка аккаунтов и групп, а состав group.json
// заполняется финальными ID — ссылки остаются согласованными.
package migrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
	"standings-edu/internal/source"
	"standings-edu/internal/storage"
	"standings-edu/internal/studentintake"
)

// BundleVersion — версия формата бандла. v2 добавил явные поля manual_tables
// (оценки кондуитов) и manual_grades (ручные оценки столбцов); v1-бандлы
// (таблицы кондуитов были внутри provider_config) читаются как раньше через
// извлечение таблицы из конфига при импорте. v3 добавил flag_reviews (проверки
// флагов нечестности); v2-бандлы просто без них.
const BundleVersion = 3

// Selection задаёт, для каких групп брать участников и/или контесты. Нулевое
// значение (обе карты nil) означает «всё»: все группы, и участники, и контесты.
// Непустая карта — включать только те слаги, что в ней стоят true.
type Selection struct {
	Participants map[string]bool
	Contests     map[string]bool
}

func (s Selection) all() bool { return s.Participants == nil && s.Contests == nil }

func (s Selection) wantParticipants(slug string) bool {
	if s.all() {
		return true
	}
	return s.Participants[slug]
}

func (s Selection) wantContests(slug string) bool {
	if s.all() {
		return true
	}
	return s.Contests[slug]
}

// includesGroup — группа вообще участвует (есть хоть один включённый аспект).
func (s Selection) includesGroup(slug string) bool {
	if s.all() {
		return true
	}
	return s.Participants[slug] || s.Contests[slug]
}

// Bundle — переносимый набор: группы + их ученики + глобальные контесты.
type Bundle struct {
	Version    int               `json:"version"`
	ExportedAt time.Time         `json:"exported_at"`
	Students   []domain.Student  `json:"students,omitempty"`
	Contests   []json.RawMessage `json:"contests,omitempty"`
	// ManualTables — оценки глобальных кондуитов: contest_id -> TSV-таблица
	// (из data/manual_tables.json). Хранятся отдельно от определений контестов.
	ManualTables map[string]string `json:"manual_tables,omitempty"`
	Groups       []BundleGroup     `json:"groups"`
}

// BundleGroup — одна группа: её group.json и записи contests.json (как есть).
type BundleGroup struct {
	Slug     string            `json:"slug"`
	Group    json.RawMessage   `json:"group"`
	Contests []json.RawMessage `json:"contests,omitempty"`
	// ManualTables — оценки inline-кондуитов группы (contest_id -> TSV).
	ManualTables map[string]string `json:"manual_tables,omitempty"`
	// ManualGrades — ручные оценки столбцов группы (grades_manual.json):
	// column_id -> student_id -> оценка.
	ManualGrades map[string]map[string]float64 `json:"manual_grades,omitempty"`
	// FlagReviews — проверки флагов нечестности участников группы (из
	// data/flag_reviews.json). student_id — id на стороне экспорта: при импорте
	// он ремапится через ФИО на финальный id, как и состав группы.
	FlagReviews []BundleFlagReview `json:"flag_reviews,omitempty"`
}

// BundleFlagReview — одна отметка проверки флага в бандле.
type BundleFlagReview struct {
	StudentID string            `json:"student_id"`
	FlagKey   string            `json:"flag_key"`
	Review    domain.FlagReview `json:"review"`
}

// Report — итог импорта для показа пользователю.
type Report struct {
	StudentsAdded   int
	StudentsUpdated int
	ContestsAdded   int
	Groups          []GroupReport
	Warnings        []string
}

// GroupReport — что произошло с одной группой при импорте.
type GroupReport struct {
	Slug          string
	Created       bool
	StudentsAdded int
	ContestsAdded int
	MembersAdded  int
	// GradesAdded — сколько новых ячеек ручных оценок (столбец,ученик) добавлено.
	GradesAdded int
	// FlagReviewsAdded — сколько новых проверок флагов нечестности добавлено.
	FlagReviewsAdded int
}

// BuildBundle собирает бандл из data-директории по выбору sel. Группы-участницы
// объединённых групп добавляются автоматически и целиком. includeTokens=false
// вырезает group_secret_token и panel_access из group.json.
func BuildBundle(dataDir string, sel Selection, includeTokens bool) (*Bundle, error) {
	groupsDir := filepath.Join(dataDir, "groups")

	// Эффективные флаги по каждой группе (участники/контесты).
	effP := make(map[string]bool)
	effC := make(map[string]bool)
	seed := make([]string, 0)
	if sel.all() {
		entries, err := os.ReadDir(groupsDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && domain.IsValidSlug(e.Name()) {
				seed = append(seed, e.Name())
				effP[e.Name()] = true
				effC[e.Name()] = true
			}
		}
	} else {
		for slug, v := range sel.Participants {
			if v && domain.IsValidSlug(slug) {
				effP[slug] = true
			}
		}
		for slug, v := range sel.Contests {
			if v && domain.IsValidSlug(slug) {
				effC[slug] = true
			}
		}
		for slug := range effP {
			seed = append(seed, slug)
		}
		for slug := range effC {
			if !effP[slug] {
				seed = append(seed, slug)
			}
		}
	}

	// Обходим выбранные группы и добавляем участниц объединённых — целиком.
	order := make([]string, 0)
	inOrder := make(map[string]bool)
	sort.Strings(seed)
	queue := append([]string{}, seed...)
	for len(queue) > 0 {
		slug := queue[0]
		queue = queue[1:]
		if inOrder[slug] {
			continue
		}
		inOrder[slug] = true
		order = append(order, slug)
		raw, err := os.ReadFile(filepath.Join(groupsDir, slug, "group.json"))
		if err != nil {
			continue
		}
		var gf domain.GroupFile
		if json.Unmarshal(raw, &gf) != nil {
			continue
		}
		for _, m := range gf.MemberGroups {
			m = strings.TrimSpace(m)
			if !domain.IsValidSlug(m) || inOrder[m] {
				continue
			}
			effP[m] = true // авто-участница объединённой группы — целиком
			effC[m] = true
			queue = append(queue, m)
		}
	}
	sort.Strings(order)

	bundle := &Bundle{Version: BundleVersion, ExportedAt: time.Now().UTC()}
	groupsByStudent := make(map[string][]string)
	contestIDs := make(map[string]struct{})

	// Проверки флагов нечестности — глобальны по ученику (ключ: ученик|ключ
	// флага); в бандл группы попадают отметки её участников.
	flagReviews, err := storage.LoadFlagReviews(dataDir)
	if err != nil {
		return nil, fmt.Errorf("load flag reviews: %w", err)
	}
	reviewsByStudent := make(map[string][]BundleFlagReview)
	for key, rev := range flagReviews {
		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		reviewsByStudent[parts[0]] = append(reviewsByStudent[parts[0]], BundleFlagReview{
			StudentID: parts[0], FlagKey: parts[1], Review: rev,
		})
	}
	for _, list := range reviewsByStudent {
		sort.Slice(list, func(i, j int) bool { return list[i].FlagKey < list[j].FlagKey })
	}

	for _, slug := range order {
		origRaw, err := os.ReadFile(filepath.Join(groupsDir, slug, "group.json"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		groupRaw := maybeStripToken(origRaw, includeTokens)

		var gf domain.GroupFile
		_ = json.Unmarshal(origRaw, &gf)
		if effP[slug] {
			for _, sid := range gf.StudentIDs {
				if sid = strings.TrimSpace(sid); sid != "" {
					groupsByStudent[sid] = append(groupsByStudent[sid], slug)
				}
			}
		} else {
			groupRaw = stripKey(groupRaw, "student_ids") // состав не выгружаем
		}

		bg := BundleGroup{Slug: slug, Group: json.RawMessage(groupRaw)}
		if effC[slug] {
			if err := fileutil.ReadJSON(filepath.Join(groupsDir, slug, "contests.json"), &bg.Contests); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				bg.Contests = nil
			}
			for _, e := range bg.Contests {
				if id := rawStringField(e, "id"); id != "" {
					contestIDs[id] = struct{}{}
				}
			}
			// Оценки inline-кондуитов группы (отдельный файл, не в конфиге).
			if t := loadManualTablesQuiet(filepath.Join(groupsDir, slug, source.ManualTablesFileName)); len(t) > 0 {
				bg.ManualTables = t
			}
		}
		// Ручные оценки столбцов и проверки флагов — при экспорте участников группы.
		if effP[slug] {
			if g := loadManualGradesQuiet(filepath.Join(groupsDir, slug, "grades_manual.json")); len(g) > 0 {
				bg.ManualGrades = g
			}
			for _, sid := range gf.StudentIDs {
				bg.FlagReviews = append(bg.FlagReviews, reviewsByStudent[strings.TrimSpace(sid)]...)
			}
		}

		bundle.Groups = append(bundle.Groups, bg)
	}

	// Ученики выбранных (по участникам) групп с проставленным членством.
	var allStudents []domain.Student
	if err := fileutil.ReadJSON(filepath.Join(dataDir, "students.json"), &allStudents); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, s := range allStudents {
		slugs, ok := groupsByStudent[strings.TrimSpace(s.ID)]
		if !ok {
			continue
		}
		sort.Strings(slugs)
		s.Groups = slugs
		bundle.Students = append(bundle.Students, s)
	}

	// Глобальные контесты, на которые ссылаются группы (у которых берём контесты).
	var allContests []json.RawMessage
	if err := fileutil.ReadJSON(filepath.Join(dataDir, "contests.json"), &allContests); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	globalTables := loadManualTablesQuiet(filepath.Join(dataDir, source.ManualTablesFileName))
	for _, c := range allContests {
		id := rawStringField(c, "id")
		if id == "" {
			continue
		}
		if _, ok := contestIDs[id]; !ok {
			continue
		}
		bundle.Contests = append(bundle.Contests, c) // определение без оценок
		if t, ok := globalTables[id]; ok {
			if bundle.ManualTables == nil {
				bundle.ManualTables = map[string]string{}
			}
			bundle.ManualTables[id] = t
		}
	}

	return bundle, nil
}

// loadManualTablesQuiet читает manual_tables.json (map id -> TSV); нет файла —
// пустая карта.
func loadManualTablesQuiet(path string) map[string]string {
	tables := map[string]string{}
	if err := fileutil.ReadJSON(path, &tables); err != nil {
		return map[string]string{}
	}
	return tables
}

// loadManualGradesQuiet читает grades_manual.json (col -> student -> оценка);
// нет файла — пустая карта.
func loadManualGradesQuiet(path string) map[string]map[string]float64 {
	grades := map[string]map[string]float64{}
	if err := fileutil.ReadJSON(path, &grades); err != nil {
		return map[string]map[string]float64{}
	}
	return grades
}

// legacyTableFromContest извлекает таблицу кондуита, оставшуюся в provider_config
// (бандлы v1). Возвращает (id, таблица, true) для кондуита с непустой таблицей.
func legacyTableFromContest(raw json.RawMessage) (string, string, bool) {
	id := rawStringField(raw, "id")
	if id == "" || rawStringField(raw, "provider") != source.ManualTableProviderID {
		return "", "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "", "", false
	}
	_, table, had, err := source.StripManualTable(m["provider_config"])
	if err != nil || !had || strings.TrimSpace(table) == "" {
		return "", "", false
	}
	return id, table, true
}

// stripLegacyTable убирает таблицу из provider_config кондуита (чистое
// определение; оценки хранятся в manual_tables.json). Гарантирует task_count,
// чтобы пустой конфиг оставался валидным. Не-кондуит/без таблицы — как есть.
func stripLegacyTable(raw json.RawMessage) json.RawMessage {
	if rawStringField(raw, "provider") != source.ManualTableProviderID {
		return raw
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	cfg, table, had, err := source.StripManualTable(m["provider_config"])
	if err != nil {
		return raw
	}
	if had {
		var mm map[string]any
		_ = json.Unmarshal(cfg, &mm)
		if mm == nil {
			mm = map[string]any{}
		}
		if v, ok := mm["task_count"].(float64); !ok || v < 1 {
			labels, _ := source.SplitManualTable(table, 0)
			mm["task_count"] = len(labels)
		}
		cfg, err = json.Marshal(mm)
		if err != nil {
			return raw
		}
	}
	m["provider_config"] = cfg
	out, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return out
}

// mergeManualTablesIntoFile объединяет таблицы кондуитов в файл (max по ячейкам),
// создавая его при необходимости.
func mergeManualTablesIntoFile(path string, incoming map[string]string) error {
	if len(incoming) == 0 {
		return nil
	}
	existing := loadManualTablesQuiet(path)
	for id, t := range incoming {
		existing[id] = source.MergeManualTablesMax(existing[id], t)
	}
	return fileutil.WriteJSON(path, existing, 0o644)
}

// mergeManualGradesIntoFile объединяет ручные оценки столбцов в файл, беря по
// каждой ячейке максимум. Возвращает число новых (столбец,ученик).
func mergeManualGradesIntoFile(path string, incoming map[string]map[string]float64) (int, error) {
	if len(incoming) == 0 {
		return 0, nil
	}
	existing := loadManualGradesQuiet(path)
	added := 0
	for col, byStud := range incoming {
		if existing[col] == nil {
			existing[col] = map[string]float64{}
		}
		for sid, v := range byStud {
			if cur, ok := existing[col][sid]; !ok || v > cur {
				if !ok {
					added++
				}
				existing[col][sid] = v
			}
		}
	}
	return added, fileutil.WriteJSON(path, existing, 0o644)
}

// collectGroupManualTables собирает таблицы кондуитов группы для импорта:
// явные из bg.ManualTables плюс легаси, оставшиеся в конфигах контестов.
func collectManualTables(explicit map[string]string, contests []json.RawMessage) map[string]string {
	out := map[string]string{}
	for id, t := range explicit {
		out[id] = t
	}
	for _, c := range contests {
		if id, t, ok := legacyTableFromContest(c); ok {
			out[id] = source.MergeManualTablesMax(out[id], t)
		}
	}
	return out
}

// ImportBundle дописывает бандл в data-директорию по выбору sel. Существующее не
// перезаписывается.
func ImportBundle(dataDir string, b *Bundle, sel Selection) (*Report, error) {
	if b == nil {
		return nil, fmt.Errorf("пустой бандл")
	}
	rep := &Report{}

	// 1) Ученики — как при merge анкет; членство фильтруем по выбранным группам.
	students := make([]domain.Student, 0, len(b.Students))
	for _, s := range b.Students {
		if strings.TrimSpace(s.FullName) == "" {
			rep.Warnings = append(rep.Warnings, "пропущен ученик без ФИО (id="+s.ID+")")
			continue
		}
		kept := make([]string, 0, len(s.Groups))
		for _, g := range s.Groups {
			if sel.wantParticipants(strings.TrimSpace(g)) {
				kept = append(kept, g)
			}
		}
		if len(kept) == 0 {
			continue // ни одна его группа не выбрана по участникам
		}
		s.Groups = kept
		students = append(students, s)
	}
	var merged []domain.Student
	if len(students) > 0 {
		studentsPath := filepath.Join(dataDir, "students.json")
		var existing []domain.Student
		if err := fileutil.ReadJSON(studentsPath, &existing); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		m, stats, err := studentintake.MergeStudents(existing, students)
		if err != nil {
			return nil, err
		}
		merged = m
		rep.StudentsAdded = stats.Added
		rep.StudentsUpdated = stats.Updated
		if err := studentintake.WriteStudentsFile(studentsPath, merged); err != nil {
			return nil, err
		}
	}

	// 2) Глобальные контесты — только те, что нужны группам с выбранными контестами.
	wantedContest := make(map[string]struct{})
	for _, bg := range b.Groups {
		if !sel.wantContests(strings.TrimSpace(bg.Slug)) {
			continue
		}
		for _, c := range bg.Contests {
			if id := rawStringField(c, "id"); id != "" {
				wantedContest[id] = struct{}{}
			}
		}
	}
	contests := make([]json.RawMessage, 0, len(b.Contests))
	for _, c := range b.Contests {
		if id := rawStringField(c, "id"); id != "" {
			if _, ok := wantedContest[id]; ok {
				contests = append(contests, c)
			}
		}
	}
	// Оценки глобальных кондуитов: явные из бандла (по нужным контестам) +
	// легаси из конфигов; сливаем в глобальный manual_tables.json (max), даже
	// если само определение контеста уже есть (тогда пропускается ниже).
	wantedTables := map[string]string{}
	for id, t := range b.ManualTables {
		if _, ok := wantedContest[id]; ok {
			wantedTables[id] = t
		}
	}
	globalTables := collectManualTables(wantedTables, contests)
	// В contests.json пишем чистые определения (без таблицы в конфиге).
	cleanContests := make([]json.RawMessage, len(contests))
	for i, c := range contests {
		cleanContests[i] = stripLegacyTable(c)
	}
	if err := importGlobalContests(dataDir, cleanContests, rep); err != nil {
		return nil, err
	}
	if err := mergeManualTablesIntoFile(filepath.Join(dataDir, source.ManualTablesFileName), globalTables); err != nil {
		return nil, err
	}

	// 3) Группы: настройки/объединения/контесты. Состав заполнит AddStudentsToGroups.
	preCount := make(map[string]int)
	for _, bg := range b.Groups {
		slug := strings.TrimSpace(bg.Slug)
		if !domain.IsValidSlug(slug) {
			rep.Warnings = append(rep.Warnings, "пропущена группа с некорректным slug: "+bg.Slug)
			continue
		}
		isCombined := bundleGroupIsCombined(bg)
		if !sel.wantParticipants(slug) && !sel.wantContests(slug) && !isCombined {
			continue // группа полностью снята с импорта
		}
		preCount[slug] = groupStudentCount(dataDir, slug)
		if err := importGroupSettings(dataDir, slug, bg, sel.wantContests(slug), sel.wantParticipants(slug), rep); err != nil {
			return nil, err
		}
	}

	// 4) Состав групп — тем же механизмом, что и merge анкет.
	if len(students) > 0 {
		if err := studentintake.AddStudentsToGroups(dataDir, merged, students); err != nil {
			return nil, err
		}
	}

	// 5) Сколько учеников добавилось в каждую группу.
	for i := range rep.Groups {
		slug := rep.Groups[i].Slug
		rep.Groups[i].StudentsAdded = groupStudentCount(dataDir, slug) - preCount[slug]
	}

	// 6) Проверки флагов нечестности: id ремапится через ФИО (как состав групп),
	// существующие отметки не перезаписываются.
	if err := importFlagReviews(dataDir, b, sel, students, rep); err != nil {
		return nil, err
	}

	return rep, nil
}

// importFlagReviews сливает проверки флагов из бандла в data/flag_reviews.json.
// student_id бандла переводится в финальный id через ФИО (та же логика, что у
// состава групп); отметки под уже занятыми ключами не трогаются.
func importFlagReviews(dataDir string, b *Bundle, sel Selection, bundleStudents []domain.Student, rep *Report) error {
	nameByBundleID := make(map[string]string, len(bundleStudents))
	for _, s := range bundleStudents {
		n := domain.NormalizeStudent(s)
		if strings.TrimSpace(n.ID) != "" && n.FullName != "" {
			nameByBundleID[strings.TrimSpace(n.ID)] = n.FullName
		}
	}

	var finalStudents []domain.Student
	if err := fileutil.ReadJSON(filepath.Join(dataDir, "students.json"), &finalStudents); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	finalIDByName := make(map[string]string, len(finalStudents))
	for _, s := range domain.NormalizeStudents(finalStudents) {
		if s.FullName != "" && strings.TrimSpace(s.ID) != "" {
			finalIDByName[s.FullName] = s.ID
		}
	}

	current, err := storage.LoadFlagReviews(dataDir)
	if err != nil {
		return fmt.Errorf("load flag reviews: %w", err)
	}
	changed := false
	for _, bg := range b.Groups {
		slug := strings.TrimSpace(bg.Slug)
		if len(bg.FlagReviews) == 0 || !sel.wantParticipants(slug) || !domain.IsValidSlug(slug) {
			continue
		}
		added := 0
		for _, br := range bg.FlagReviews {
			name := nameByBundleID[strings.TrimSpace(br.StudentID)]
			if name == "" {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("группа %s: проверка флага для неизвестного ученика id=%s пропущена", slug, br.StudentID))
				continue
			}
			finalID, ok := finalIDByName[name]
			if !ok {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("группа %s: проверка флага — ученик %q не найден после merge", slug, name))
				continue
			}
			flagKey := strings.TrimSpace(br.FlagKey)
			rev := br.Review
			// Нормализация ключа по составу эпизода — как в LoadFlagReviews.
			if rev.Flag != nil && len(rev.Flag.TaskURLs) > 0 {
				flagKey = domain.CourseFlagKey(rev.Flag.TaskURLs)
				snap := *rev.Flag
				snap.Key = flagKey
				rev.Flag = &snap
			}
			if flagKey == "" {
				continue
			}
			key := domain.FlagReviewKey(finalID, flagKey)
			if _, exists := current[key]; exists {
				continue // не перезаписываем существующие отметки
			}
			current[key] = rev
			added++
		}
		if added > 0 {
			changed = true
			found := false
			for i := range rep.Groups {
				if rep.Groups[i].Slug == slug {
					rep.Groups[i].FlagReviewsAdded = added
					found = true
					break
				}
			}
			if !found {
				rep.Groups = append(rep.Groups, GroupReport{Slug: slug, FlagReviewsAdded: added})
			}
		}
	}
	if !changed {
		return nil
	}
	return fileutil.WriteJSON(filepath.Join(dataDir, "flag_reviews.json"), current, 0o644)
}

func importGlobalContests(dataDir string, contests []json.RawMessage, rep *Report) error {
	if len(contests) == 0 {
		return nil
	}
	path := filepath.Join(dataDir, "contests.json")
	var existing []json.RawMessage
	if err := fileutil.ReadJSON(path, &existing); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	have := make(map[string]struct{}, len(existing))
	for _, c := range existing {
		if id := rawStringField(c, "id"); id != "" {
			have[id] = struct{}{}
		}
	}
	for _, c := range contests {
		id := rawStringField(c, "id")
		if id == "" {
			continue
		}
		if _, ok := have[id]; ok {
			continue
		}
		existing = append(existing, c)
		have[id] = struct{}{}
		rep.ContestsAdded++
	}
	if existing == nil {
		existing = []json.RawMessage{}
	}
	return fileutil.WriteJSON(path, existing, 0o644)
}

func importGroupSettings(dataDir, slug string, bg BundleGroup, importContests, importParticipants bool, rep *Report) error {
	gr := GroupReport{Slug: slug}
	dir := filepath.Join(dataDir, "groups", slug)
	groupPath := filepath.Join(dir, "group.json")
	contestsPath := filepath.Join(dir, "contests.json")
	tablesPath := filepath.Join(dir, source.ManualTablesFileName)
	gradesPath := filepath.Join(dir, "grades_manual.json")

	// Чистые определения контестов группы (таблицы кондуитов — отдельно).
	cleanContests := make([]json.RawMessage, len(bg.Contests))
	for i, c := range bg.Contests {
		cleanContests[i] = stripLegacyTable(c)
	}

	existingRaw, err := os.ReadFile(groupPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Новая группа — создаём. student_ids вычищаем: состав добавит
		// AddStudentsToGroups финальными ID.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeRawPretty(groupPath, stripKey(bg.Group, "student_ids")); err != nil {
			return err
		}
		contests := []json.RawMessage{}
		if importContests && cleanContests != nil {
			contests = cleanContests
		}
		if err := fileutil.WriteJSON(contestsPath, contests, 0o644); err != nil {
			return err
		}
		var gf domain.GroupFile
		_ = json.Unmarshal(bg.Group, &gf)
		gr.Created = true
		gr.MembersAdded = len(gf.MemberGroups)
		gr.ContestsAdded = len(contests)

	case err != nil:
		return err

	default:
		// Существующая группа — дописываем объединение/скрытые контесты; контесты
		// по флагу. Название/токен/оценки/состав не трогаем.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(existingRaw, &m); err != nil {
			rep.Warnings = append(rep.Warnings, "группа "+slug+": не разобрать group.json, пропущена")
			return nil
		}
		var inc domain.GroupFile
		_ = json.Unmarshal(bg.Group, &inc)
		gr.MembersAdded = mergeStringArrayKey(m, "member_groups", inc.MemberGroups)
		mergeStringArrayKey(m, "hidden_contests", inc.HiddenContests)
		if err := fileutil.WriteJSON(groupPath, m, 0o644); err != nil {
			return err
		}

		if importContests {
			var existingC []json.RawMessage
			if err := fileutil.ReadJSON(contestsPath, &existingC); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			have := make(map[string]struct{}, len(existingC))
			for _, c := range existingC {
				if id := rawStringField(c, "id"); id != "" {
					have[id] = struct{}{}
				}
			}
			for _, c := range cleanContests {
				id := rawStringField(c, "id")
				if id == "" {
					continue
				}
				if _, ok := have[id]; ok {
					continue
				}
				existingC = append(existingC, c)
				have[id] = struct{}{}
				gr.ContestsAdded++
			}
			if existingC == nil {
				existingC = []json.RawMessage{}
			}
			if err := fileutil.WriteJSON(contestsPath, existingC, 0o644); err != nil {
				return err
			}
		}
	}

	// Оценки кондуитов группы (inline): сливаем всегда, когда переносим контесты —
	// max по ячейкам, даже если контест уже был на целевом сервере.
	if importContests {
		tables := collectManualTables(bg.ManualTables, bg.Contests)
		if err := mergeManualTablesIntoFile(tablesPath, tables); err != nil {
			return err
		}
	}
	// Ручные оценки столбцов — при переносе участников; max по ячейкам.
	if importParticipants {
		added, err := mergeManualGradesIntoFile(gradesPath, bg.ManualGrades)
		if err != nil {
			return err
		}
		gr.GradesAdded = added
	}

	rep.Groups = append(rep.Groups, gr)
	return nil
}

// --- вспомогательное ---

func bundleGroupIsCombined(bg BundleGroup) bool {
	var gf domain.GroupFile
	if json.Unmarshal(bg.Group, &gf) != nil {
		return false
	}
	return len(gf.MemberGroups) > 0
}

func groupStudentCount(dataDir, slug string) int {
	b, err := os.ReadFile(filepath.Join(dataDir, "groups", slug, "group.json"))
	if err != nil {
		return 0
	}
	var gf domain.GroupFile
	if json.Unmarshal(b, &gf) != nil {
		return 0
	}
	return len(gf.StudentIDs)
}

func rawStringField(raw json.RawMessage, field string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func maybeStripToken(raw []byte, include bool) []byte {
	if include {
		return raw
	}
	// Учётки панели группы — такой же секрет, как токен: из бандла вырезаем.
	return stripKey(stripKey(raw, "group_secret_token"), "panel_access")
}

// stripKey удаляет ключ верхнего уровня из JSON-объекта, сохраняя остальные поля.
func stripKey(raw json.RawMessage, key string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	if _, ok := m[key]; !ok {
		return raw
	}
	delete(m, key)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return raw
	}
	return b
}

// mergeStringArrayKey дописывает в строковый массив m[key] новые значения из
// incoming (без дублей, сохраняя порядок). Возвращает число добавленных.
func mergeStringArrayKey(m map[string]json.RawMessage, key string, incoming []string) int {
	if len(incoming) == 0 {
		return 0
	}
	existing := parseStringArray(m[key])
	seen := make(map[string]struct{}, len(existing))
	for _, s := range existing {
		seen[s] = struct{}{}
	}
	added := 0
	for _, s := range incoming {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		existing = append(existing, s)
		seen[s] = struct{}{}
		added++
	}
	if added > 0 {
		if b, err := json.Marshal(existing); err == nil {
			m[key] = b
		}
	}
	return added
}

func parseStringArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func writeRawPretty(path string, raw json.RawMessage) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	return fileutil.WriteJSON(path, v, 0o644)
}
