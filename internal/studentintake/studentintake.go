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
)

var ErrMissingFullName = errors.New("full_name is required")
var ErrInvalidGroupSlug = errors.New("invalid group slug")

type MergeStats struct {
	Updated int
	Added   int
}

type Store struct {
	intakePath string
	dataDir    string
	mu         sync.Mutex
}

func NewStore(path string, dataDir ...string) *Store {
	dir := filepath.Dir(path)
	if len(dataDir) > 0 && strings.TrimSpace(dataDir[0]) != "" {
		dir = strings.TrimSpace(dataDir[0])
	}
	return &Store{intakePath: path, dataDir: dir}
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

	intake, err := LoadStudentsFile(s.intakePath)
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

	updatedIntake, savedIntake, _, err := upsertStudent(intake, intakeStudent, upsertOptions{
		MatchByID:               false,
		PreferIncomingIDOnEmpty: false,
	})
	if err != nil {
		return domain.Student{}, err
	}

	if submitted.Group != "" {
		if err := s.syncGroup(savedIntake, submitted.Group); err != nil {
			return domain.Student{}, err
		}
	}

	if err := WriteStudentsFile(s.intakePath, updatedIntake); err != nil {
		return domain.Student{}, fmt.Errorf("write intake file: %w", err)
	}
	return savedIntake, nil
}

// PrepareAdminIntakeStaging ensures a non-empty staging file for manual admin merge.
// If staging is empty or missing, it is atomically filled from the current intake file.
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

	mode, err := detectFileMode(stagingPath, 0o644)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomically(stagingPath, sourceBody, mode); err != nil {
		return nil, fmt.Errorf("write staging file %q: %w", stagingPath, err)
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

	mode, err := detectFileMode(stagingPath, 0o644)
	if err != nil {
		return err
	}
	if err := writeFileAtomically(stagingPath, body, mode); err != nil {
		return fmt.Errorf("write staging file %q: %w", stagingPath, err)
	}
	return nil
}

func (s *Store) syncGroup(intakeStudent domain.Student, groupSlug string) error {
	sourcePath := filepath.Join(s.dataDir, "students.json")
	sourceStudents, err := LoadStudentsFile(sourcePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("load source students: %w", err)
		}
		sourceStudents = nil
	}

	sourceIncoming := intakeStudent
	sourceIncoming.Groups = []string{groupSlug}

	sourceStudents, savedSource, _, err := upsertStudent(sourceStudents, sourceIncoming, upsertOptions{
		MatchByID:               true,
		PreferIncomingIDOnEmpty: true,
	})
	if err != nil {
		return err
	}

	if err := WriteStudentsFile(sourcePath, sourceStudents); err != nil {
		return fmt.Errorf("write source students: %w", err)
	}

	groupPath, groupFile, err := loadOrCreateGroupFile(s.dataDir, groupSlug)
	if err != nil {
		return fmt.Errorf("load group %q: %w", groupSlug, err)
	}

	groupFile.StudentIDs = mergeGroups(groupFile.StudentIDs, []string{savedSource.ID})
	if err := writeGroupFile(groupPath, groupFile); err != nil {
		return fmt.Errorf("write group file %q: %w", groupPath, err)
	}
	return nil
}

func MergeStudents(existing []domain.Student, intake []domain.Student) ([]domain.Student, MergeStats, error) {
	result := normalizeStudents(existing)
	stats := MergeStats{}

	for i, incoming := range intake {
		var updated bool
		var err error
		result, _, updated, err = upsertStudent(result, incoming, upsertOptions{
			MatchByID:               true,
			PreferIncomingIDOnEmpty: false,
		})
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
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	var students []domain.Student
	if err := json.Unmarshal(b, &students); err != nil {
		return nil, fmt.Errorf("decode json %q: %w", path, err)
	}
	return students, nil
}

func LoadIntakeFile(path string) ([]domain.Student, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file %q: %w", path, err)
	}

	var items []map[string]json.RawMessage
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode json %q: %w", path, err)
	}

	out := make([]domain.Student, 0, len(items))
	for i, item := range items {
		student, decodeErr := decodeIntakeItem(item)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode intake item #%d in %q: %w", i, path, decodeErr)
		}
		if student.FullName == "" {
			return nil, fmt.Errorf("intake item #%d in %q has empty full_name", i, path)
		}
		out = append(out, student)
	}
	return out, nil
}

func WriteStudentsFile(path string, students []domain.Student) error {
	normalized := normalizeStudents(students)

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

	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json %q: %w", path, err)
	}
	b = append(b, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write file %q: %w", path, err)
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
	fullName := normalizeWhitespace(fields["full_name"])
	if fullName == "" {
		return submittedFields{}, ErrMissingFullName
	}

	return submittedFields{
		FullName:   fullName,
		PublicName: normalizeWhitespace(fields["public_name"]),
		Group:      strings.TrimSpace(fields["group"]),
		Accounts:   accountsFromFields(fields),
	}, nil
}

type upsertOptions struct {
	MatchByID               bool
	PreferIncomingIDOnEmpty bool
}

func upsertStudent(
	students []domain.Student,
	incoming domain.Student,
	opts upsertOptions,
) ([]domain.Student, domain.Student, bool, error) {
	out := normalizeStudents(students)
	incoming = normalizeStudent(incoming)
	if incoming.FullName == "" {
		return nil, domain.Student{}, false, ErrMissingFullName
	}

	idx := findStudentIndexByFullName(out, incoming.FullName)
	if idx < 0 && opts.MatchByID && incoming.ID != "" {
		idx = findStudentIndexByID(out, incoming.ID)
	}

	if idx >= 0 {
		merged := out[idx]
		merged.FullName = incoming.FullName
		if incoming.PublicName != "" {
			merged.PublicName = incoming.PublicName
		} else if merged.PublicName == "" {
			merged.PublicName = GeneratePublicNameFromFullName(merged.FullName)
		}
		merged.Accounts = mergeAccounts(merged.Accounts, incoming.Accounts)
		merged.Groups = mergeGroups(merged.Groups, incoming.Groups)

		currentID := strings.TrimSpace(merged.ID)
		if currentID == "" || idTakenByOther(out, idx, currentID) {
			if opts.PreferIncomingIDOnEmpty {
				if candidate := normalizeID(incoming.ID); candidate != "" && !idTakenByOther(out, idx, candidate) {
					merged.ID = candidate
				} else {
					merged.ID = nextUniqueID(out, merged.FullName, idx)
				}
			} else {
				merged.ID = nextUniqueID(out, merged.FullName, idx)
			}
		}

		merged = normalizeStudent(merged)
		out[idx] = merged
		return out, merged, true, nil
	}

	created := incoming
	created.ID = normalizeID(created.ID)
	if created.ID == "" || idTakenByOther(out, -1, created.ID) {
		created.ID = nextUniqueID(out, created.FullName, -1)
	}
	if created.PublicName == "" {
		created.PublicName = GeneratePublicNameFromFullName(created.FullName)
	}
	created = normalizeStudent(created)

	out = append(out, created)
	return out, created, false, nil
}

func normalizeStudents(students []domain.Student) []domain.Student {
	out := make([]domain.Student, len(students))
	for i, s := range students {
		out[i] = normalizeStudent(s)
	}
	return out
}

func normalizeStudent(s domain.Student) domain.Student {
	s.ID = normalizeID(s.ID)
	s.FullName = normalizeWhitespace(s.FullName)
	s.PublicName = normalizeWhitespace(s.PublicName)
	s.Accounts = normalizeAccounts(s.Accounts)
	s.Groups = normalizeGroups(s.Groups)
	return s
}

func normalizeID(id string) string {
	return strings.TrimSpace(id)
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func normalizeAccounts(accounts []domain.Account) []domain.Account {
	if len(accounts) == 0 {
		return nil
	}

	out := make([]domain.Account, 0, len(accounts))
	indexBySite := make(map[string]int, len(accounts))
	for _, a := range accounts {
		site := domain.NormalizeSite(a.Site)
		accountID := strings.TrimSpace(a.AccountID)
		if site == "" || accountID == "" {
			continue
		}
		if idx, ok := indexBySite[site]; ok {
			out[idx].AccountID = accountID
			continue
		}
		indexBySite[site] = len(out)
		out = append(out, domain.Account{Site: site, AccountID: accountID})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeAccounts(existing []domain.Account, updates []domain.Account) []domain.Account {
	if len(updates) == 0 {
		return normalizeAccounts(existing)
	}

	merged := make([]domain.Account, 0, len(existing)+len(updates))
	indexBySite := make(map[string]int, len(existing)+len(updates))

	for _, acc := range normalizeAccounts(existing) {
		indexBySite[acc.Site] = len(merged)
		merged = append(merged, acc)
	}
	for _, update := range normalizeAccounts(updates) {
		if idx, ok := indexBySite[update.Site]; ok {
			merged[idx].AccountID = update.AccountID
			continue
		}
		indexBySite[update.Site] = len(merged)
		merged = append(merged, update)
	}

	return merged
}

func accountsFromFields(fields map[string]string) []domain.Account {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	accounts := make([]domain.Account, 0, len(keys))
	for _, key := range keys {
		field := strings.TrimSpace(key)
		switch field {
		case "", "full_name", "public_name", "id", "group", "groups":
			continue
		}

		accountID := strings.TrimSpace(fields[key])
		if accountID == "" {
			continue
		}
		accounts = append(accounts, domain.Account{
			Site:      domain.NormalizeSite(field),
			AccountID: accountID,
		})
	}
	return normalizeAccounts(accounts)
}

func normalizeGroups(groups []string) []string {
	if len(groups) == 0 {
		return nil
	}

	out := make([]string, 0, len(groups))
	seen := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if _, ok := seen[group]; ok {
			continue
		}
		seen[group] = struct{}{}
		out = append(out, group)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeGroups(existing []string, updates []string) []string {
	return normalizeGroups(append(normalizeGroups(existing), normalizeGroups(updates)...))
}

func findStudentIndexByFullName(students []domain.Student, fullName string) int {
	for i := range students {
		if students[i].FullName == fullName {
			return i
		}
	}
	return -1
}

func findStudentIndexByID(students []domain.Student, id string) int {
	id = strings.TrimSpace(id)
	for i := range students {
		if strings.TrimSpace(students[i].ID) == id {
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
		Update:     boolPtr(true),
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
	groupFile.StudentIDs = normalizeGroups(groupFile.StudentIDs)

	b, err := json.MarshalIndent(groupFile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal group file %q: %w", path, err)
	}
	b = append(b, '\n')

	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
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

	extraAccounts := make([]domain.Account, 0)
	extraKeys := make([]string, 0, len(item))
	for key := range item {
		extraKeys = append(extraKeys, key)
	}
	sort.Strings(extraKeys)

	for _, key := range extraKeys {
		field := strings.TrimSpace(strings.ToLower(key))
		switch field {
		case "", "id", "full_name", "public_name", "accounts", "groups", "group":
			continue
		}

		var value string
		if err := json.Unmarshal(item[key], &value); err != nil {
			return domain.Student{}, fmt.Errorf("field %q: expected string value", key)
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		extraAccounts = append(extraAccounts, domain.Account{Site: field, AccountID: value})
	}

	student = normalizeStudent(student)
	student.Accounts = mergeAccounts(student.Accounts, extraAccounts)
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

func detectFileMode(path string, defaultMode os.FileMode) (os.FileMode, error) {
	info, err := os.Stat(path)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return defaultMode, nil
	}
	return 0, fmt.Errorf("stat file %q: %w", path, err)
}

func writeFileAtomically(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	tmpFile, err := os.CreateTemp(dir, ".studentintake-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	cleanup := func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmpFile.Write(body); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

func boolPtr(v bool) *bool {
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
	parts := strings.Fields(normalizeWhitespace(fullName))
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
	parts := strings.Fields(normalizeWhitespace(fullName))
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
