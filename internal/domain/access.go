package domain

// Доступы к группам: вместо трёх зашитых ролей — список записей с набором прав.
// Запись живёт либо в group.json группы (локальный доступ), либо в
// data/credentials/global_accesses.json (глобальный, с областью действия на все
// или выбранные группы). Вход — по ссылке с токеном либо по логину и паролю.
//
// Права проверяются точечно (see Perm), поэтому «уровень» доступа — это просто
// набор галочек; пресеты (см. Presets) повторяют прежние роли.

import (
	"fmt"
	"sort"
	"strings"
)

// Perm — одно разрешение. Значения стабильны: лежат в JSON.
type Perm string

const (
	// Просмотр.
	PermViewUnfrozen     Perm = "view.unfrozen"     // полные версии замороженных таблиц
	PermViewHidden       Perm = "view.hidden"       // скрытые контесты и задачи
	PermViewTaskLinks    Perm = "view.task_links"   // ссылки на условия, даже если ученикам выключены
	PermViewParticipants Perm = "view.participants" // статистика участников и профили
	PermViewExport       Perm = "view.export"       // выгрузка в Excel
	PermViewJudgeLinks   Perm = "view.judge_links"  // ejudge в режиме судьи

	// Оценки.
	PermGradesManual  Perm = "grades.manual"  // выставлять ручные оценки
	PermGradesConfig  Perm = "grades.config"  // настраивать столбцы, веса, коэффициенты
	PermKonduitFill   Perm = "konduit.fill"   // заполнять кондуиты
	PermKonduitCreate Perm = "konduit.create" // создавать кондуиты

	// Честность.
	PermFlagsReview Perm = "flags.review" // размечать флаги нечестности

	// Контесты группы.
	PermContestsManage Perm = "contests.manage" // добавить/убрать/переставить/настроить
	PermContestsInline Perm = "contests.inline" // создавать и править inline-контесты

	// Только для глобальных доступов.
	PermViewDirectory Perm = "view.directory" // каталог групп со ссылками
)

// PermGroup — раздел прав для формы редактирования.
type PermGroup struct {
	Title string
	Perms []PermInfo
}

// PermInfo — право с человекочитаемым описанием (для UI и подсказок).
type PermInfo struct {
	Perm  Perm
	Title string
	Hint  string
	// GlobalOnly — право имеет смысл только у глобального доступа.
	GlobalOnly bool
}

// PermCatalog — все права по разделам, в порядке показа в форме.
func PermCatalog() []PermGroup {
	return []PermGroup{
		{Title: "Просмотр", Perms: []PermInfo{
			{PermViewUnfrozen, "Размороженные таблицы", "Видеть результаты замороженных контестов полностью.", false},
			{PermViewHidden, "Скрытые контесты и задачи", "Видеть то, что скрыто от учеников.", false},
			{PermViewTaskLinks, "Ссылки на условия", "Видеть ссылки на задачи, даже если ученикам они выключены.", false},
			{PermViewParticipants, "Статистика участников", "Страница участников, профили учеников, темп курса.", false},
			{PermViewExport, "Выгрузка в Excel", "Скачивать таблицы книгой Excel.", false},
			{PermViewJudgeLinks, "Судейские ссылки ejudge", "Задачи ejudge открываются в режиме судьи (new-judge).", false},
		}},
		{Title: "Оценки", Perms: []PermInfo{
			{PermGradesManual, "Выставлять оценки", "Заполнять ручные столбцы таблицы оценок.", false},
			{PermGradesConfig, "Настраивать таблицу оценок", "Столбцы, веса, метрика, нормировка, дорешка.", false},
			{PermKonduitFill, "Заполнять кондуиты", "Редактировать таблицы ручных отметок.", false},
			{PermKonduitCreate, "Создавать кондуиты", "Заводить новые таблицы ручных отметок.", false},
		}},
		{Title: "Честность", Perms: []PermInfo{
			{PermFlagsReview, "Размечать флаги", "Отмечать эпизоды «сам решил» / «перенос» / «нарушение».", false},
		}},
		{Title: "Контесты группы", Perms: []PermInfo{
			{PermContestsManage, "Управлять контестами", "Добавлять, убирать, переставлять и настраивать записи группы.", false},
			{PermContestsInline, "Свои (inline) контесты", "Создавать и редактировать контесты внутри группы.", false},
		}},
		{Title: "Каталог", Perms: []PermInfo{
			{PermViewDirectory, "Каталог групп", "Список доступных групп со ссылками на них.", true},
		}},
	}
}

// KnownPerm — право существует (защита от опечаток в JSON).
func KnownPerm(p Perm) bool {
	for _, g := range PermCatalog() {
		for _, info := range g.Perms {
			if info.Perm == p {
				return true
			}
		}
	}
	return false
}

// PermTitle — название права для интерфейса (для чипов в списке доступов).
func PermTitle(p Perm) string {
	for _, g := range PermCatalog() {
		for _, info := range g.Perms {
			if info.Perm == p {
				return info.Title
			}
		}
	}
	return string(p)
}

// PermSet — набор прав с быстрой проверкой и объединением.
type PermSet map[Perm]struct{}

func NewPermSet(perms ...Perm) PermSet {
	set := make(PermSet, len(perms))
	for _, p := range perms {
		set[p] = struct{}{}
	}
	return set
}

func (s PermSet) Has(p Perm) bool {
	if s == nil {
		return false
	}
	_, ok := s[p]
	return ok
}

// HasAny — есть хотя бы одно из перечисленных прав.
func (s PermSet) HasAny(perms ...Perm) bool {
	for _, p := range perms {
		if s.Has(p) {
			return true
		}
	}
	return false
}

// Add добавляет права другого набора (объединение доступов).
func (s PermSet) Add(perms []Perm) {
	for _, p := range perms {
		s[p] = struct{}{}
	}
}

// Empty — прав нет вовсе (доступ ничего не даёт сверх публичного вида).
func (s PermSet) Empty() bool { return len(s) == 0 }

// Sorted — права в порядке каталога (для стабильного показа и записи).
func (s PermSet) Sorted() []Perm {
	out := make([]Perm, 0, len(s))
	for _, g := range PermCatalog() {
		for _, info := range g.Perms {
			if s.Has(info.Perm) {
				out = append(out, info.Perm)
			}
		}
	}
	return out
}

// Способы входа.
const (
	AccessAuthToken    = "token"
	AccessAuthPassword = "password"
)

// Область действия глобального доступа.
const (
	AccessScopeAll    = "all"
	AccessScopeGroups = "groups"
)

// AccessEntry — одна запись доступа.
type AccessEntry struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Enabled — доступ включён. nil/absent — включён (чтобы старые записи и
	// ручная правка JSON вели себя ожидаемо).
	Enabled *bool `json:"enabled,omitempty"`
	// Auth — "token" (ссылка ?token=…) или "password" (логин и пароль).
	Auth     string `json:"auth"`
	Token    string `json:"token,omitempty"`
	Login    string `json:"login,omitempty"`
	Password string `json:"password,omitempty"`
	// Scope/Groups — только у глобальных доступов: "all" — все группы,
	// "groups" — перечисленные слаги.
	Scope  string   `json:"scope,omitempty"`
	Groups []string `json:"groups,omitempty"`
	Perms  []Perm   `json:"perms"`
	// Legacy — запись собрана из старых полей (group_secret_token, panel_access,
	// directory_credentials) и в файле её нет. Не сериализуется.
	Legacy bool `json:"-"`
}

// IsEnabled — доступ активен (nil считается «включён»).
func (a *AccessEntry) IsEnabled() bool {
	return a != nil && (a.Enabled == nil || *a.Enabled)
}

// UsesPassword / UsesToken — способ входа.
func (a *AccessEntry) UsesPassword() bool {
	return a != nil && a.Auth == AccessAuthPassword && strings.TrimSpace(a.Login) != "" && a.Password != ""
}

func (a *AccessEntry) UsesToken() bool {
	return a != nil && a.Auth == AccessAuthToken && strings.TrimSpace(a.Token) != ""
}

// PermSet — права записи набором.
func (a *AccessEntry) PermSet() PermSet {
	if a == nil {
		return nil
	}
	return NewPermSet(a.Perms...)
}

// CoversGroup — доступ распространяется на группу. У локального доступа области
// нет (он живёт в самой группе) — считаем, что покрывает свою.
func (a *AccessEntry) CoversGroup(slug string) bool {
	if a == nil {
		return false
	}
	switch a.Scope {
	case "":
		return true // локальный доступ
	case AccessScopeAll:
		return true
	case AccessScopeGroups:
		for _, g := range a.Groups {
			if strings.EqualFold(strings.TrimSpace(g), slug) {
				return true
			}
		}
	}
	return false
}

// Validate проверяет запись перед сохранением. global=true — разрешены область
// действия и права каталога.
func (a *AccessEntry) Validate(global bool) error {
	if a == nil {
		return fmt.Errorf("пустая запись доступа")
	}
	a.Title = NormalizeWhitespace(a.Title)
	if a.Title == "" {
		return fmt.Errorf("укажите название доступа")
	}
	switch a.Auth {
	case AccessAuthToken:
		if strings.TrimSpace(a.Token) == "" {
			return fmt.Errorf("доступ %q: нет токена", a.Title)
		}
		a.Login, a.Password = "", ""
	case AccessAuthPassword:
		a.Login = strings.TrimSpace(a.Login)
		if a.Login == "" || a.Password == "" {
			return fmt.Errorf("доступ %q: укажите логин и пароль", a.Title)
		}
		a.Token = ""
	default:
		return fmt.Errorf("доступ %q: способ входа должен быть «token» или «password»", a.Title)
	}

	perms := make([]Perm, 0, len(a.Perms))
	seen := make(map[Perm]struct{}, len(a.Perms))
	for _, p := range a.Perms {
		if !KnownPerm(p) {
			return fmt.Errorf("доступ %q: неизвестное право %q", a.Title, p)
		}
		if !global && p == PermViewDirectory {
			return fmt.Errorf("доступ %q: право каталога бывает только у глобального доступа", a.Title)
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		perms = append(perms, p)
	}
	if len(perms) == 0 {
		return fmt.Errorf("доступ %q: отметьте хотя бы одно право", a.Title)
	}
	a.Perms = NewPermSet(perms...).Sorted()

	if !global {
		a.Scope, a.Groups = "", nil
		return nil
	}
	switch a.Scope {
	case AccessScopeAll:
		a.Groups = nil
	case AccessScopeGroups:
		groups := make([]string, 0, len(a.Groups))
		for _, g := range a.Groups {
			g = strings.TrimSpace(g)
			if g != "" && IsValidSlug(g) {
				groups = append(groups, g)
			}
		}
		sort.Strings(groups)
		a.Groups = groups
		if len(a.Groups) == 0 && !NewPermSet(a.Perms...).Has(PermViewDirectory) {
			return fmt.Errorf("доступ %q: выберите хотя бы одну группу", a.Title)
		}
	default:
		return fmt.Errorf("доступ %q: область должна быть «all» или «groups»", a.Title)
	}
	return nil
}

// ── Пресеты ─────────────────────────────────────────────────────────────────

// AccessPreset — заготовка набора прав.
type AccessPreset struct {
	ID    string
	Title string
	Hint  string
	Perms []Perm
	// GlobalOnly — пресет предлагается только для глобальных доступов.
	GlobalOnly bool
}

// ObserverPerms — права пресета «Наблюдатель» (полный просмотр без изменений).
func ObserverPerms() []Perm {
	return []Perm{
		PermViewUnfrozen, PermViewHidden, PermViewTaskLinks,
		PermViewParticipants, PermViewExport, PermViewJudgeLinks,
	}
}

// JuryPerms — «Жюри»: просмотр + оценки, кондуиты, флаги.
func JuryPerms() []Perm {
	return append(ObserverPerms(),
		PermGradesManual, PermGradesConfig, PermKonduitFill, PermKonduitCreate, PermFlagsReview)
}

// AdminPerms — «Админ»: жюри + управление контестами группы.
func AdminPerms() []Perm {
	return append(JuryPerms(), PermContestsManage, PermContestsInline)
}

// Presets — заготовки для формы редактирования доступа.
func Presets() []AccessPreset {
	return []AccessPreset{
		{ID: "observer", Title: "Наблюдатель", Hint: "Полный просмотр: разморозка, скрытое, участники, экспорт. Ничего не меняет.", Perms: ObserverPerms()},
		{ID: "jury", Title: "Жюри", Hint: "Наблюдатель + оценки, кондуиты и разметка флагов.", Perms: JuryPerms()},
		{ID: "admin", Title: "Админ", Hint: "Жюри + управление контестами группы.", Perms: AdminPerms()},
		{ID: "curator", Title: "Куратор", Hint: "Каталог групп + полный просмотр в них.", Perms: append(ObserverPerms(), PermViewDirectory), GlobalOnly: true},
		{ID: "directory", Title: "Только каталог", Hint: "Список групп со ссылками, без доступа к самим таблицам.", Perms: []Perm{PermViewDirectory}, GlobalOnly: true},
	}
}

// PresetIDFor — id пресета, точно совпадающего с набором прав ("" — свой набор).
func PresetIDFor(perms []Perm) string {
	set := NewPermSet(perms...)
	for _, preset := range Presets() {
		want := NewPermSet(preset.Perms...)
		if len(want) != len(set) {
			continue
		}
		same := true
		for p := range want {
			if !set.Has(p) {
				same = false
				break
			}
		}
		if same {
			return preset.ID
		}
	}
	return ""
}
