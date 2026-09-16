package studentintake

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"standings-edu/internal/domain"
	"standings-edu/internal/fileutil"
)

var ErrMissingFullName = errors.New("full_name is required")
var ErrInvalidGroupSlug = errors.New("invalid group slug")

type MergeStats struct {
	Updated int
	Added   int
}

type Store struct {
	intakePath string
	mu         sync.Mutex
}

func NewStore(path string) *Store {
	return &Store{intakePath: path}
}

func (s *Store) Submit(fields map[string]string) (domain.Student, error) {
	submitted, err := parseSubmittedFields(fields)
	if err != nil {
		return domain.Student{}, err
	}
	if submitted.Group != "" && !domain.IsValidSlug(submitted.Group) {
		return domain.Student{}, ErrInvalidGroupSlug
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Тот же загрузчик, что и в merge: единый формат чтения intake (включая
	// сокращённые поля-аккаунты), чтобы новый submit не терял ранее записанные данные.
	intake, err := LoadIntakeFile(s.intakePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return domain.Student{}, fmt.Errorf("load intake file: %w", err)
		}
		intake = nil
	}

	intakeStudent := domain.Student{
		FullName:   submitted.FullName,
		PublicName: submitted.PublicName,
		Accounts:   submitted.Accounts,
		Groups:     nil,
	}
	if submitted.Group != "" {
		intakeStudent.Groups = []string{submitted.Group}
	}

	// Анкета попадает ТОЛЬКО в intake-файл. Перенос ученика в data/students.json
	// и привязка к группе происходят позже, на этапе подтверждения (merge intake).
	// Используется та же общая логика merge, что и при слиянии intake с основной базой.
	updatedIntake, savedIntake, _, err := mergeStudent(intake, intakeStudent)
	if err != nil {
		return domain.Student{}, err
	}

	if err := WriteStudentsFile(s.intakePath, updatedIntake); err != nil {
		return domain.Student{}, fmt.Errorf("write intake file: %w", err)
	}
	return savedIntake, nil
}

// AddStudentsToGroups привязывает учеников из intake к их группам:
// для каждой пары (ученик, группа) из intake добавляет финальный ID ученика
// (из объединённого students.json) в data/groups/<slug>/group.json, создавая
// скелет группы при отсутствии.
func AddStudentsToGroups(dataDir string, mergedStudents []domain.Student, intakeStudents []domain.Student) error {
	if len(intakeStudents) == 0 {
		return nil
	}

	idByFullName := make(map[string]string, len(mergedStudents))
	for _, student := range domain.NormalizeStudents(mergedStudents) {
		if student.FullName == "" || strings.TrimSpace(student.ID) == "" {
			continue
		}
		idByFullName[student.FullName] = student.ID
	}

	additions := make(map[string][]string)
	slugOrder := make([]string, 0)
	for i, raw := range intakeStudents {
		student := domain.NormalizeStudent(raw)
		if len(student.Groups) == 0 {
			continue
		}
		studentID, ok := idByFullName[student.FullName]
		if !ok {
			return fmt.Errorf("intake item #%d (%q): merged student not found", i, student.FullName)
		}
		for _, slug := range student.Groups {
			if !domain.IsValidSlug(slug) {
				return fmt.Errorf("intake item #%d (%q): invalid group slug %q", i, student.FullName, slug)
			}
			if _, seen := additions[slug]; !seen {
				slugOrder = append(slugOrder, slug)
			}
			additions[slug] = append(additions[slug], studentID)
		}
	}

	sort.Strings(slugOrder)
	for _, slug := range slugOrder {
		groupPath, groupFile, err := loadOrCreateGroupFile(dataDir, slug)
		if err != nil {
			return fmt.Errorf("load group %q: %w", slug, err)
		}
		groupFile.StudentIDs = domain.MergeGroups(groupFile.StudentIDs, additions[slug])
		if err := writeGroupFile(groupPath, groupFile); err != nil {
			return fmt.Errorf("write group file %q: %w", groupPath, err)
		}
	}
	return nil
}

// MergePreviewGroup — привязка ученика к группе в превью merge.
type MergePreviewGroup struct {
	Slug          string `json:"slug"`
	AlreadyMember bool   `json:"already_member"`
}

// MergeAccountConflict — аккаунт анкеты, который после merge оказался ещё и у
// ДРУГОГО ученика (одна учётка на разных сайтах указана двум людям — вероятно
// опечатка или чужой аккаунт).
type MergeAccountConflict struct {
	Site      string `json:"site"`
	AccountID string `json:"account_id"`
	OtherID   string `json:"other_id"`
	OtherName string `json:"other_name"`
}

// MergePreviewStudent — одна анкета в превью: во что она разрешается.
type MergePreviewStudent struct {
	FullName  string                 `json:"full_name"`
	FinalID   string                 `json:"final_id"`
	IsNew     bool                   `json:"is_new"`
	Accounts  []domain.Account       `json:"accounts,omitempty"`
	Groups    []MergePreviewGroup    `json:"groups,omitempty"`
	Conflicts []MergeAccountConflict `json:"conflicts,omitempty"`
}

// MergePreview — результат пробного merge (dry-run): что и куда будет привязано,
// без записи на диск.
type MergePreview struct {
	Added    int                   `json:"added"`
	Updated  int                   `json:"updated"`
	Students []MergePreviewStudent `json:"students"`
}

// BuildMergePreview считает пробный merge: для каждой анкеты — финальный ID,
// новый ли это ученик, и в какие группы он попадёт (и не состоит ли уже).
// Ничего не пишет.
func BuildMergePreview(dataDir string, existing, intake []domain.Student) (MergePreview, error) {
	result := domain.NormalizeStudents(existing)
	preview := MergePreview{Students: make([]MergePreviewStudent, 0, len(intake))}

	// Текущее (и накопленное в рамках превью) членство групп.
	memberOf := make(map[string]map[string]struct{})
	members := func(slug string) map[string]struct{} {
		if m, ok := memberOf[slug]; ok {
			return m
		}
		m := make(map[string]struct{})
		if gf, err := readGroupFile(filepath.Join(dataDir, "groups", slug, "group.json")); err == nil {
			for _, id := range domain.NormalizeGroups(gf.StudentIDs) {
				m[id] = struct{}{}
			}
		}
		memberOf[slug] = m
		return m
	}

	for i, incoming := range intake {
		var saved domain.Student
		var updated bool
		var err error
		result, saved, updated, err = mergeStudent(result, incoming)
		if err != nil {
			return MergePreview{}, fmt.Errorf("intake item #%d: %w", i, err)
		}
		if updated {
			preview.Updated++
		} else {
			preview.Added++
		}

		norm := domain.NormalizeStudent(incoming)
		ps := MergePreviewStudent{FullName: saved.FullName, FinalID: saved.ID, IsNew: !updated, Accounts: norm.Accounts}
		for _, slug := range norm.Groups {
			if !domain.IsValidSlug(slug) {
				return MergePreview{}, fmt.Errorf("intake item #%d (%q): invalid group slug %q", i, saved.FullName, slug)
			}
			set := members(slug)
			_, already := set[saved.ID]
			ps.Groups = append(ps.Groups, MergePreviewGroup{Slug: slug, AlreadyMember: already})
			set[saved.ID] = struct{}{}
		}
		preview.Students = append(preview.Students, ps)
	}

	// Коллизии аккаунтов: после merge одна и та же учётка (site+account_id) может
	// оказаться у разных учеников — предупреждаем, с кем именно совпало.
	fillAccountConflicts(preview.Students, result)
	return preview, nil
}

// accountKey нормализует учётку для сравнения: сайт и id без регистра/пробелов
// (handle codeforces и login ejudge регистронезависимы).
func accountKey(a domain.Account) string {
	return domain.NormalizeSite(a.Site) + "\x00" + strings.ToLower(strings.TrimSpace(a.AccountID))
}

func fillAccountConflicts(students []MergePreviewStudent, result []domain.Student) {
	owners := make(map[string]map[string]struct{})
	nameByID := make(map[string]string, len(result))
	for _, s := range result {
		nameByID[s.ID] = s.FullName
		for _, a := range s.Accounts {
			if strings.TrimSpace(a.AccountID) == "" {
				continue
			}
			key := accountKey(a)
			if owners[key] == nil {
				owners[key] = make(map[string]struct{})
			}
			owners[key][s.ID] = struct{}{}
		}
	}

	for i := range students {
		ps := &students[i]
		seenOther := make(map[string]struct{})
		for _, a := range ps.Accounts {
			if strings.TrimSpace(a.AccountID) == "" {
				continue
			}
			for ownerID := range owners[accountKey(a)] {
				if ownerID == ps.FinalID {
					continue
				}
				dedup := accountKey(a) + "\x00" + ownerID
				if _, dup := seenOther[dedup]; dup {
					continue
				}
				seenOther[dedup] = struct{}{}
				ps.Conflicts = append(ps.Conflicts, MergeAccountConflict{
					Site:      a.Site,
					AccountID: a.AccountID,
					OtherID:   ownerID,
					OtherName: nameByID[ownerID],
				})
			}
		}
		sort.Slice(ps.Conflicts, func(a, b int) bool {
			if ps.Conflicts[a].Site != ps.Conflicts[b].Site {
				return ps.Conflicts[a].Site < ps.Conflicts[b].Site
			}
			return ps.Conflicts[a].OtherName < ps.Conflicts[b].OtherName
		})
	}
}

func MergeStudents(existing []domain.Student, intake []domain.Student) ([]domain.Student, MergeStats, error) {
	result := domain.NormalizeStudents(existing)
	stats := MergeStats{}

	for i, incoming := range intake {
		var updated bool
		var err error
		result, _, updated, err = mergeStudent(result, incoming)
		if err != nil {
			return nil, MergeStats{}, fmt.Errorf("intake item #%d: %w", i, err)
		}
		if updated {
			stats.Updated++
		} else {
			stats.Added++
		}
	}

	return result, stats, nil
}

func LoadStudentsFile(path string) ([]domain.Student, error) {
	var students []domain.Student
	if err := fileutil.ReadJSON(path, &students); err != nil {
		return nil, err
	}
	return students, nil
}

func LoadIntakeFile(path string) ([]domain.Student, error) {
	var items []map[string]json.RawMessage
	if err := fileutil.ReadJSON(path, &items); err != nil {
		return nil, err
	}
	return parseIntakeItems(items)
}

// ParseIntakeBytes разбирает intake из сырого JSON (для превью merge из
// содержимого редактора, ещё не сохранённого на диск).
func ParseIntakeBytes(body []byte) ([]domain.Student, error) {
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode intake: %w", err)
	}
	return parseIntakeItems(items)
}

func parseIntakeItems(items []map[string]json.RawMessage) ([]domain.Student, error) {
	out := make([]domain.Student, 0, len(items))
	for i, item := range items {
		student, decodeErr := decodeIntakeItem(item)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode intake item #%d: %w", i, decodeErr)
		}
		if student.FullName == "" {
			return nil, fmt.Errorf("intake item #%d has empty full_name", i)
		}
		out = append(out, student)
	}
	return out, nil
}

// studentItems приводит учеников к форме записи на диск. Форма общая для
// students.json и очереди анкет: decodeIntakeItem читает ровно эти поля.
func studentItems(students []domain.Student) []studentJSON {
	normalized := domain.NormalizeStudents(students)

	items := make([]studentJSON, 0, len(normalized))
	for _, s := range normalized {
		item := studentJSON{ID: s.ID, FullName: s.FullName}
		if s.PublicName != "" {
			item.PublicName = s.PublicName
		}
		if len(s.Accounts) > 0 {
			item.Accounts = s.Accounts
		}
		if len(s.Groups) > 0 {
			item.Groups = s.Groups
		}
		items = append(items, item)
	}
	return items
}

func WriteStudentsFile(path string, students []domain.Student) error {
	if err := fileutil.WriteJSON(path, studentItems(students), 0o644); err != nil {
		return fmt.Errorf("write students %q: %w", path, err)
	}
	return nil
}

type submittedFields struct {
	FullName   string
	PublicName string
	Group      string
	Accounts   []domain.Account
}

func parseSubmittedFields(fields map[string]string) (submittedFields, error) {
	fullName := domain.NormalizeWhitespace(fields["full_name"])
	if fullName == "" {
		return submittedFields{}, ErrMissingFullName
	}

	return submittedFields{
		FullName:   fullName,
		PublicName: domain.NormalizeWhitespace(fields["public_name"]),
		Group:      strings.TrimSpace(fields["group"]),
		Accounts:   accountsFromStringFields(fields),
	}, nil
}

// mergeStudent — единая логика merge, используемая и при добавлении одной анкеты
// в intake, и при сливании intake с основной базой.
//
// Философия:
//   - ФИО считаем переданными верно и сопоставляем записи ТОЛЬКО по ФИО;
//   - совпадение по ФИО → обновляем только переданные поля: public_name (если
//     непустой), аккаунты и группы доливаем (новые значения для того же site
//     перезаписывают старые); существующий id сохраняем;
//   - нет совпадения → добавляем новую запись (первое заполнение формы).
//
// Возвращает обновлённый список, итоговую запись и флаг «была ли это правка».
func mergeStudent(students []domain.Student, incoming domain.Student) ([]domain.Student, domain.Student, bool, error) {
	out := domain.NormalizeStudents(students)
	incoming = domain.NormalizeStudent(incoming)
	if incoming.FullName == "" {
		return nil, domain.Student{}, false, ErrMissingFullName
	}

	if idx := findStudentIndexByFullName(out, incoming.FullName); idx >= 0 {
		merged := out[idx]
		if incoming.PublicName != "" {
			merged.PublicName = incoming.PublicName
		} else if merged.PublicName == "" {
			merged.PublicName = GeneratePublicNameFromFullName(merged.FullName)
		}
		merged.Accounts = domain.MergeAccounts(merged.Accounts, incoming.Accounts)
		merged.Groups = domain.MergeGroups(merged.Groups, incoming.Groups)
		merged.ID = ensureStudentID(out, idx, merged.ID, merged.FullName)
		merged = domain.NormalizeStudent(merged)
		out[idx] = merged
		return out, merged, true, nil
	}

	created := incoming
	created.ID = ensureStudentID(out, -1, created.ID, created.FullName)
	if created.PublicName == "" {
		created.PublicName = GeneratePublicNameFromFullName(created.FullName)
	}
	created = domain.NormalizeStudent(created)

	out = append(out, created)
	return out, created, false, nil
}

// ensureStudentID сохраняет текущий id, если он непустой и свободен; иначе
// генерирует уникальный id из ФИО.
func ensureStudentID(students []domain.Student, idx int, currentID, fullName string) string {
	id := domain.NormalizeID(currentID)
	if id != "" && !idTakenByOther(students, idx, id) {
		return id
	}
	return nextUniqueID(students, fullName, idx)
}

// reservedStudentFieldKeys — ключи, которые не являются аккаунтами: любое другое
// строковое поле считается аккаунтом (site=имя поля, account_id=значение).
var reservedStudentFieldKeys = map[string]struct{}{
	"":            {},
	"id":          {},
	"full_name":   {},
	"public_name": {},
	"accounts":    {},
	"group":       {},
	"groups":      {},
	"token":       {}, // секрет приёма анкет, не аккаунт
}

func isReservedStudentField(key string) bool {
	_, ok := reservedStudentFieldKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

// accountsFromStringFields превращает «плоские» строковые поля в аккаунты.
// Единая логика для анкеты из RPC и для произвольных полей в intake-файле.
func accountsFromStringFields(fields map[string]string) []domain.Account {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	accounts := make([]domain.Account, 0, len(keys))
	for _, key := range keys {
		if isReservedStudentField(key) {
			continue
		}
		accountID := strings.TrimSpace(fields[key])
		if accountID == "" {
			continue
		}
		accounts = append(accounts, domain.Account{
			Site:      domain.NormalizeSite(key),
			AccountID: accountID,
		})
	}
	return domain.NormalizeAccounts(accounts)
}

func findStudentIndexByFullName(students []domain.Student, fullName string) int {
	for i := range students {
		if students[i].FullName == fullName {
			return i
		}
	}
	return -1
}

func nextUniqueID(students []domain.Student, fullName string, currentIdx int) string {
	return GenerateUniqueID(fullName, func(id string) bool {
		return idTakenByOther(students, currentIdx, id)
	})
}

func idTakenByOther(students []domain.Student, currentIdx int, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for i := range students {
		if i == currentIdx {
			continue
		}
		if strings.TrimSpace(students[i].ID) == id {
			return true
		}
	}
	return false
}

func loadOrCreateGroupFile(dataDir, groupSlug string) (string, domain.GroupFile, error) {
	groupDir := filepath.Join(dataDir, "groups", groupSlug)
	path := filepath.Join(groupDir, "group.json")

	groupFile, err := readGroupFile(path)
	if err == nil {
		if err := ensureGroupContestsFile(groupDir); err != nil {
			return "", domain.GroupFile{}, err
		}
		return path, groupFile, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", domain.GroupFile{}, err
	}

	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		return "", domain.GroupFile{}, fmt.Errorf("mkdir group dir %q: %w", groupDir, err)
	}

	groupFile = domain.GroupFile{
		Title:      groupSlug,
		Update:     pointerTo(true),
		StudentIDs: nil,
	}
	if err := writeGroupFile(path, groupFile); err != nil {
		return "", domain.GroupFile{}, err
	}
	if err := ensureGroupContestsFile(groupDir); err != nil {
		return "", domain.GroupFile{}, err
	}
	return path, groupFile, nil
}

func readGroupFile(path string) (domain.GroupFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return domain.GroupFile{}, err
	}

	var groupFile domain.GroupFile
	if err := json.Unmarshal(b, &groupFile); err != nil {
		return domain.GroupFile{}, fmt.Errorf("decode group file %q: %w", path, err)
	}
	return groupFile, nil
}

func writeGroupFile(path string, groupFile domain.GroupFile) error {
	groupFile.StudentIDs = domain.NormalizeGroups(groupFile.StudentIDs)
	if err := fileutil.WriteJSON(path, groupFile, 0o644); err != nil {
		return fmt.Errorf("write group file %q: %w", path, err)
	}
	return nil
}

func ensureGroupContestsFile(groupDir string) error {
	path := filepath.Join(groupDir, "contests.json")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat contests file %q: %w", path, err)
	}

	if err := os.WriteFile(path, []byte("[]\n"), 0o644); err != nil {
		return fmt.Errorf("write contests file %q: %w", path, err)
	}
	return nil
}

type studentJSON struct {
	ID         string           `json:"id"`
	FullName   string           `json:"full_name"`
	PublicName string           `json:"public_name,omitempty"`
	Accounts   []domain.Account `json:"accounts,omitempty"`
	Groups     []string         `json:"groups,omitempty"`
}

func decodeIntakeItem(item map[string]json.RawMessage) (domain.Student, error) {
	student := domain.Student{}

	if raw, ok := item["id"]; ok {
		if err := json.Unmarshal(raw, &student.ID); err != nil {
			return domain.Student{}, fmt.Errorf("field id: %w", err)
		}
	}
	if raw, ok := item["full_name"]; ok {
		if err := json.Unmarshal(raw, &student.FullName); err != nil {
			return domain.Student{}, fmt.Errorf("field full_name: %w", err)
		}
	}
	if raw, ok := item["public_name"]; ok {
		if err := json.Unmarshal(raw, &student.PublicName); err != nil {
			return domain.Student{}, fmt.Errorf("field public_name: %w", err)
		}
	}
	if raw, ok := item["accounts"]; ok {
		if err := json.Unmarshal(raw, &student.Accounts); err != nil {
			return domain.Student{}, fmt.Errorf("field accounts: %w", err)
		}
	}
	if raw, ok := item["groups"]; ok {
		if err := json.Unmarshal(raw, &student.Groups); err != nil {
			return domain.Student{}, fmt.Errorf("field groups: %w", err)
		}
	}

	extraFields := make(map[string]string, len(item))
	for key, raw := range item {
		if isReservedStudentField(key) {
			continue
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return domain.Student{}, fmt.Errorf("field %q: expected string value", key)
		}
		extraFields[key] = value
	}

	student = domain.NormalizeStudent(student)
	student.Accounts = domain.MergeAccounts(student.Accounts, accountsFromStringFields(extraFields))
	return student, nil
}

func isEmptyIntakeFile(body []byte) bool {
	if len(bytes.TrimSpace(body)) == 0 {
		return true
	}
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		return false
	}
	return len(items) == 0
}

func pointerTo(v bool) *bool {
	return &v
}

var translitTable = map[rune]string{
	'а': "a",
	'б': "b",
	'в': "v",
	'г': "g",
	'д': "d",
	'е': "e",
	'ё': "e",
	'ж': "zh",
	'з': "z",
	'и': "i",
	'й': "y",
	'к': "k",
	'л': "l",
	'м': "m",
	'н': "n",
	'о': "o",
	'п': "p",
	'р': "r",
	'с': "s",
	'т': "t",
	'у': "u",
	'ф': "f",
	'х': "h",
	'ц': "ts",
	'ч': "ch",
	'ш': "sh",
	'щ': "sch",
	'ъ': "",
	'ы': "y",
	'ь': "",
	'э': "e",
	'ю': "yu",
	'я': "ya",
}

func GenerateIDFromFullName(fullName string) string {
	parts := strings.Fields(domain.NormalizeWhitespace(fullName))
	if len(parts) == 0 {
		return "student"
	}

	base := slugifyASCII(transliterate(parts[0]))
	if base == "" {
		base = "student"
	}

	initials := make([]string, 0, 2)
	if len(parts) > 1 {
		if initial := firstInitial(parts[1]); initial != "" {
			initials = append(initials, initial)
		}
	}
	if len(parts) > 2 {
		if initial := firstInitial(parts[2]); initial != "" {
			initials = append(initials, initial)
		}
	}

	id := base
	if len(initials) > 0 {
		id = base + "-" + strings.Join(initials, "")
	}

	id = slugifyASCII(id)
	if id == "" {
		return "student"
	}
	return id
}

func GeneratePublicNameFromFullName(fullName string) string {
	parts := strings.Fields(domain.NormalizeWhitespace(fullName))
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}

	var b strings.Builder
	b.WriteString(parts[0])

	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		var initial rune
		for _, r := range part {
			initial = r
			break
		}
		if initial == 0 {
			continue
		}
		b.WriteByte(' ')
		b.WriteRune(initial)
		b.WriteByte('.')
	}

	return b.String()
}

func GenerateUniqueID(fullName string, isTaken func(id string) bool) string {
	base := GenerateIDFromFullName(fullName)
	if !isTaken(base) {
		return base
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !isTaken(candidate) {
			return candidate
		}
	}
}

func transliterate(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if mapped, ok := translitTable[r]; ok {
			b.WriteString(mapped)
			continue
		}
		if isASCIIAlphaNum(r) {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return b.String()
}

func firstInitial(part string) string {
	s := transliterate(part)
	for _, r := range s {
		if isASCIIAlphaNum(r) {
			return string(r)
		}
	}
	return ""
}

func slugifyASCII(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := true

	for _, r := range s {
		if isASCIIAlphaNum(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	return strings.Trim(b.String(), "-")
}

func isASCIIAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// Очередь анкет — один файл (s.intakePath). Анкета лежит в нём, пока её не
// приняли; приём забирает выбранные и оставляет остальные на месте.
//
// Раньше файлов было два: «свежие» и «пачка админки», куда «Подготовить»
// физически переносило анкеты. Из-за переноса новые анкеты не доходили до
// админки, пока пачка не слита, а удаление строки в редакторе теряло анкету
// совсем (единственная копия была в пачке). Пачка больше не используется;
// оставшийся от старой схемы файл вливается в очередь при первом чтении.

// IntakeSelection — какие анкеты берём.
type IntakeSelection struct {
	// Group — принимать только анкеты, поданные в эту группу (и записать
	// ученика только в неё). Пусто — без фильтра по группе (админка).
	Group string
	// FullNames — только перечисленные ФИО. nil — все подходящие.
	FullNames []string
}

// AcceptStats — итог приёма анкет.
type AcceptStats struct {
	// Accepted — сколько анкет принято.
	Accepted int
	// Created/Updated — сколько учеников заведено заново и сколько обновлено
	// (анкета сошлась по ФИО с уже существующим).
	Created int
	Updated int
	// Remaining — сколько анкет осталось в очереди.
	Remaining int
}

// loadQueueLocked читает очередь анкет. Если рядом остался непустой файл-пачка
// от старой схемы, вливает его в очередь и очищает — так данные не застревают
// в файле, которым больше никто не пользуется. Вызывать под s.mu.
func (s *Store) loadQueueLocked(stagingPath string) ([]domain.Student, error) {
	read := func(path string) ([]domain.Student, error) {
		if strings.TrimSpace(path) == "" {
			return nil, nil
		}
		list, err := LoadIntakeFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		return list, nil
	}

	queue, err := read(s.intakePath)
	if err != nil {
		return nil, err
	}
	staging, err := read(stagingPath)
	if err != nil {
		return nil, err
	}
	if len(staging) == 0 {
		return queue, nil
	}

	// Пачка впереди: она старше, а новые анкеты дополняют её аккаунтами.
	merged, _, err := MergeStudents(staging, queue)
	if err != nil {
		return nil, err
	}
	if err := writeIntakeFile(s.intakePath, merged); err != nil {
		return nil, err
	}
	if err := clearIntakeFile(stagingPath); err != nil {
		return nil, err
	}
	return merged, nil
}

// pickIntake делит очередь на выбранные (готовые к приёму) и остаток.
// Для выбранных при sel.Group != "" список групп сводится к одной: ученик
// заводится только в неё, а прочие группы анкеты остаются ждать в остатке.
func pickIntake(queue []domain.Student, sel IntakeSelection) (selected, remaining []domain.Student) {
	var want map[string]struct{}
	if sel.FullNames != nil {
		want = make(map[string]struct{}, len(sel.FullNames))
		for _, name := range sel.FullNames {
			if key := domain.NormalizeWhitespace(name); key != "" {
				want[strings.ToLower(key)] = struct{}{}
			}
		}
	}

	selected = make([]domain.Student, 0, len(queue))
	remaining = make([]domain.Student, 0, len(queue))
	for _, raw := range queue {
		student := domain.NormalizeStudent(raw)
		picked := sel.Group == "" || containsGroupSlug(student.Groups, sel.Group)
		if picked && want != nil {
			_, picked = want[strings.ToLower(student.FullName)]
		}
		if !picked {
			remaining = append(remaining, student)
			continue
		}

		accepted := student
		if sel.Group != "" {
			accepted.Groups = []string{sel.Group}
			// Анкета могла быть подана и в другие группы — там её ещё не приняли.
			if rest := groupsWithout(student.Groups, sel.Group); len(rest) > 0 {
				other := student
				other.Groups = rest
				remaining = append(remaining, other)
			}
		}
		selected = append(selected, accepted)
	}
	return selected, remaining
}

// IntakeQueue — очередь анкет (для показа на страницах).
func (s *Store) IntakeQueue(stagingPath string) ([]domain.Student, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadQueueLocked(stagingPath)
}

// PreviewIntake — что произойдёт при приёме выбранных анкет. Ничего не пишет
// (кроме возможного вливания старой пачки в очередь при первом чтении).
func (s *Store) PreviewIntake(dataDir, stagingPath string, sel IntakeSelection) (MergePreview, error) {
	if sel.Group != "" && !domain.IsValidSlug(sel.Group) {
		return MergePreview{}, ErrInvalidGroupSlug
	}
	s.mu.Lock()
	queue, err := s.loadQueueLocked(stagingPath)
	s.mu.Unlock()
	if err != nil {
		return MergePreview{}, err
	}
	selected, _ := pickIntake(queue, sel)
	if len(selected) == 0 {
		return MergePreview{}, nil
	}
	existing, err := LoadStudentsFile(filepath.Join(dataDir, "students.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return MergePreview{}, err
	}
	return BuildMergePreview(dataDir, existing, selected)
}

// AcceptIntake принимает выбранные анкеты: ученики заводятся (или находятся по
// ФИО) в общем students.json и добавляются в свои группы, а сами анкеты уходят
// из очереди. Невыбранные остаются на месте.
func (s *Store) AcceptIntake(dataDir, stagingPath string, sel IntakeSelection) (AcceptStats, error) {
	if sel.Group != "" && !domain.IsValidSlug(sel.Group) {
		return AcceptStats{}, ErrInvalidGroupSlug
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	queue, err := s.loadQueueLocked(stagingPath)
	if err != nil {
		return AcceptStats{}, err
	}
	selected, remaining := pickIntake(queue, sel)
	if len(selected) == 0 {
		return AcceptStats{Remaining: len(remaining)}, fmt.Errorf("нечего принимать: подходящих анкет нет")
	}

	studentsPath := filepath.Join(dataDir, "students.json")
	existing, err := LoadStudentsFile(studentsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AcceptStats{}, err
	}
	merged, stats, err := MergeStudents(existing, selected)
	if err != nil {
		return AcceptStats{}, err
	}
	if err := WriteStudentsFile(studentsPath, merged); err != nil {
		return AcceptStats{}, err
	}
	if err := AddStudentsToGroups(dataDir, merged, selected); err != nil {
		return AcceptStats{}, err
	}
	if err := writeIntakeFile(s.intakePath, remaining); err != nil {
		return AcceptStats{}, err
	}

	return AcceptStats{
		Accepted:  len(selected),
		Created:   stats.Added,
		Updated:   stats.Updated,
		Remaining: len(remaining),
	}, nil
}

// UpdateIntakeEntry правит анкету в очереди (опечатка в ФИО, чужая группа,
// неверный аккаунт) до приёма. origFullName — ФИО, под которым анкета лежит
// сейчас. allowedGroup != "" — правка со стороны группы: менять список групп
// нельзя (иначе доступ одной группы перекинул бы анкету в чужую), и анкета
// должна быть подана в эту группу.
func (s *Store) UpdateIntakeEntry(stagingPath, origFullName string, updated domain.Student, allowedGroup string) error {
	origKey := strings.ToLower(domain.NormalizeWhitespace(origFullName))
	if origKey == "" {
		return fmt.Errorf("не указано, какую анкету править")
	}
	updated = domain.NormalizeStudent(updated)
	if updated.FullName == "" {
		return ErrMissingFullName
	}
	for _, slug := range updated.Groups {
		if !domain.IsValidSlug(slug) {
			return fmt.Errorf("недопустимый слаг группы %q", slug)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	queue, err := s.loadQueueLocked(stagingPath)
	if err != nil {
		return err
	}
	idx := -1
	for i, st := range queue {
		if strings.ToLower(domain.NormalizeWhitespace(st.FullName)) == origKey {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("анкета %q в очереди не найдена", origFullName)
	}
	current := domain.NormalizeStudent(queue[idx])
	if allowedGroup != "" {
		if !containsGroupSlug(current.Groups, allowedGroup) {
			return fmt.Errorf("эта анкета подана не в вашу группу")
		}
		// Состав групп анкеты — не дело отдельной группы.
		updated.Groups = current.Groups
	}
	// ФИО — ключ анкеты: на совпадение с другой записью не наступаем.
	renamed := strings.ToLower(updated.FullName) != origKey
	if renamed {
		newKey := strings.ToLower(updated.FullName)
		for i, st := range queue {
			if i != idx && strings.ToLower(domain.NormalizeWhitespace(st.FullName)) == newKey {
				return fmt.Errorf("анкета на «%s» в очереди уже есть", updated.FullName)
			}
		}
	}
	// id ученика выводится из ФИО. Правят обычно как раз опечатку в нём, и
	// тащить её в постоянный id незачем: сбрасываем, чтобы при приёме он
	// сгенерировался из нового ФИО (сопоставление с базой идёт по ФИО, не по id).
	updated.ID = current.ID
	if renamed {
		updated.ID = ""
	}
	queue[idx] = updated
	return writeIntakeFile(s.intakePath, queue)
}

// DiscardIntakeEntry убирает анкету из очереди, не заводя ученика (спам,
// дубль). allowedGroup != "" — со стороны группы: анкета должна быть подана в
// неё, и вычёркивается только эта группа; в прочих группах анкета остаётся.
func (s *Store) DiscardIntakeEntry(stagingPath, fullName, allowedGroup string) error {
	key := strings.ToLower(domain.NormalizeWhitespace(fullName))
	if key == "" {
		return fmt.Errorf("не указано, какую анкету убрать")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	queue, err := s.loadQueueLocked(stagingPath)
	if err != nil {
		return err
	}
	out := make([]domain.Student, 0, len(queue))
	found := false
	for _, raw := range queue {
		student := domain.NormalizeStudent(raw)
		if strings.ToLower(student.FullName) != key {
			out = append(out, student)
			continue
		}
		found = true
		if allowedGroup == "" {
			continue // админка убирает анкету целиком
		}
		if !containsGroupSlug(student.Groups, allowedGroup) {
			return fmt.Errorf("эта анкета подана не в вашу группу")
		}
		if rest := groupsWithout(student.Groups, allowedGroup); len(rest) > 0 {
			student.Groups = rest
			out = append(out, student) // в других группах анкета остаётся
		}
	}
	if !found {
		return fmt.Errorf("анкета %q в очереди не найдена", fullName)
	}
	return writeIntakeFile(s.intakePath, out)
}

// containsGroupSlug — есть ли слаг в списке групп анкеты.
func containsGroupSlug(groups []string, slug string) bool {
	for _, g := range groups {
		if strings.EqualFold(strings.TrimSpace(g), slug) {
			return true
		}
	}
	return false
}

// groupsWithout — список групп без указанной.
func groupsWithout(groups []string, slug string) []string {
	out := make([]string, 0, len(groups))
	for _, g := range groups {
		if strings.EqualFold(strings.TrimSpace(g), slug) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// clearIntakeFile записывает пустую очередь, сохраняя права файла.
func clearIntakeFile(path string) error {
	mode, err := fileutil.DetectFileMode(path, 0o644)
	if err != nil {
		return err
	}
	if err := fileutil.WriteFileAtomic(path, []byte("[]\n"), mode); err != nil {
		return fmt.Errorf("clear intake file %q: %w", path, err)
	}
	return nil
}

// writeIntakeFile пишет очередь анкет в том же виде, в каком её читает
// LoadIntakeFile (id/full_name/public_name/accounts/groups).
func writeIntakeFile(path string, students []domain.Student) error {
	if err := fileutil.WriteJSON(path, studentItems(students), 0o644); err != nil {
		return fmt.Errorf("write intake %q: %w", path, err)
	}
	return nil
}
