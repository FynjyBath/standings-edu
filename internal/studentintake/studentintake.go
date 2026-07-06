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

func (s *Store) PrepareAdminIntakeStaging(stagingPath string) ([]byte, error) {
	stagingPath = filepath.Clean(strings.TrimSpace(stagingPath))
	if stagingPath == "" || stagingPath == "." {
		return nil, fmt.Errorf("staging path is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	stagingBody, err := os.ReadFile(stagingPath)
	if err == nil && !isEmptyIntakeFile(stagingBody) {
		return append([]byte(nil), stagingBody...), nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read staging file %q: %w", stagingPath, err)
	}

	sourceBody, err := os.ReadFile(s.intakePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read intake file %q: %w", s.intakePath, err)
		}
		sourceBody = []byte("[]\n")
	}
	if len(bytes.TrimSpace(sourceBody)) == 0 {
		sourceBody = []byte("[]\n")
	}

	mode, err := fileutil.DetectFileMode(stagingPath, 0o644)
	if err != nil {
		return nil, err
	}
	if err := fileutil.WriteFileAtomic(stagingPath, sourceBody, mode); err != nil {
		return nil, fmt.Errorf("write staging file %q: %w", stagingPath, err)
	}

	intakeMode, err := fileutil.DetectFileMode(s.intakePath, 0o644)
	if err != nil {
		return nil, err
	}
	if err := fileutil.WriteFileAtomic(s.intakePath, []byte("[]\n"), intakeMode); err != nil {
		return nil, fmt.Errorf("clear source intake file %q: %w", s.intakePath, err)
	}
	return append([]byte(nil), sourceBody...), nil
}

func (s *Store) SaveAdminIntakeStaging(stagingPath string, body []byte) error {
	stagingPath = filepath.Clean(strings.TrimSpace(stagingPath))
	if stagingPath == "" || stagingPath == "." {
		return fmt.Errorf("staging path is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	mode, err := fileutil.DetectFileMode(stagingPath, 0o644)
	if err != nil {
		return err
	}
	if err := fileutil.WriteFileAtomic(stagingPath, body, mode); err != nil {
		return fmt.Errorf("write staging file %q: %w", stagingPath, err)
	}
	return nil
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

// MergePreviewStudent — одна анкета в превью: во что она разрешается.
type MergePreviewStudent struct {
	FullName string              `json:"full_name"`
	FinalID  string              `json:"final_id"`
	IsNew    bool                `json:"is_new"`
	Accounts []domain.Account    `json:"accounts,omitempty"`
	Groups   []MergePreviewGroup `json:"groups,omitempty"`
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
	return preview, nil
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

func WriteStudentsFile(path string, students []domain.Student) error {
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

	if err := fileutil.WriteJSON(path, items, 0o644); err != nil {
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
