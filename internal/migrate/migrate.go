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
	"standings-edu/internal/studentintake"
)

// BundleVersion — версия формата бандла.
const BundleVersion = 1

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
	Groups     []BundleGroup     `json:"groups"`
}

// BundleGroup — одна группа: её group.json и записи contests.json (как есть).
type BundleGroup struct {
	Slug     string            `json:"slug"`
	Group    json.RawMessage   `json:"group"`
	Contests []json.RawMessage `json:"contests,omitempty"`
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
}

// BuildBundle собирает бандл из data-директории по выбору sel. Группы-участницы
// объединённых групп добавляются автоматически и целиком. includeTokens=false
// вырезает group_secret_token из group.json.
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

		var groupContests []json.RawMessage
		if effC[slug] {
			if err := fileutil.ReadJSON(filepath.Join(groupsDir, slug, "contests.json"), &groupContests); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				groupContests = nil
			}
			// Таблицы inline-кондуитов подставляются обратно в определение —
			// бандл самодостаточен (на диске они лежат отдельно, в manual_tables.json).
			groupTables := loadManualTablesQuiet(filepath.Join(groupsDir, slug, source.ManualTablesFileName))
			for i, e := range groupContests {
				groupContests[i] = injectManualTableRaw(e, groupTables)
				if id := rawStringField(e, "id"); id != "" {
					contestIDs[id] = struct{}{}
				}
			}
		}

		bundle.Groups = append(bundle.Groups, BundleGroup{
			Slug:     slug,
			Group:    json.RawMessage(groupRaw),
			Contests: groupContests,
		})
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
		if id := rawStringField(c, "id"); id != "" {
			if _, ok := contestIDs[id]; ok {
				bundle.Contests = append(bundle.Contests, injectManualTableRaw(c, globalTables))
			}
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

// injectManualTableRaw подставляет таблицу кондуита обратно в raw-определение
// контеста для экспорта: бандл несёт оценки с собой, а импорт на другом сервере
// работает через легаси-fallback (таблица в provider_config) и переложит её в
// manual_tables.json при первом сохранении. Не-кондуиты возвращаются как есть.
func injectManualTableRaw(raw json.RawMessage, tables map[string]string) json.RawMessage {
	id := rawStringField(raw, "id")
	if id == "" {
		return raw
	}
	table, ok := tables[id]
	if !ok {
		return raw
	}
	if rawStringField(raw, "provider") != source.ManualTableProviderID {
		return raw
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	cfg, err := source.InjectManualTable(m["provider_config"], table)
	if err != nil {
		return raw
	}
	m["provider_config"] = cfg
	out, err := json.Marshal(m)
	if err != nil {
		return raw
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
	if err := importGlobalContests(dataDir, contests, rep); err != nil {
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
		if err := importGroupSettings(dataDir, slug, bg, sel.wantContests(slug), rep); err != nil {
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

	return rep, nil
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

func importGroupSettings(dataDir, slug string, bg BundleGroup, importContests bool, rep *Report) error {
	gr := GroupReport{Slug: slug}
	dir := filepath.Join(dataDir, "groups", slug)
	groupPath := filepath.Join(dir, "group.json")
	contestsPath := filepath.Join(dir, "contests.json")

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
		if importContests && bg.Contests != nil {
			contests = bg.Contests
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
			for _, c := range bg.Contests {
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
	return stripKey(raw, "group_secret_token")
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
