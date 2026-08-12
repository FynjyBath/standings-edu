package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// Validate: чистит и проверяет запись, а неисправимое — отклоняет.
func TestAccessEntryValidate(t *testing.T) {
	bad := []struct {
		name   string
		entry  AccessEntry
		global bool
	}{
		{"без названия", AccessEntry{Auth: AccessAuthToken, Token: "t", Perms: []Perm{PermViewUnfrozen}}, false},
		{"токен пустой", AccessEntry{Title: "A", Auth: AccessAuthToken, Perms: []Perm{PermViewUnfrozen}}, false},
		{"пароль без логина", AccessEntry{Title: "A", Auth: AccessAuthPassword, Password: "p", Perms: []Perm{PermViewUnfrozen}}, false},
		{"способ входа неизвестен", AccessEntry{Title: "A", Auth: "magic", Perms: []Perm{PermViewUnfrozen}}, false},
		{"право выдумано", AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t", Perms: []Perm{"view.everything"}}, false},
		{"каталог в группе", AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t", Perms: []Perm{PermViewDirectory}}, false},
		{"без прав", AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t"}, false},
		{"область выдумана", AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t", Scope: "some", Perms: []Perm{PermViewUnfrozen}}, true},
		{"выбранные без групп", AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t", Scope: AccessScopeGroups, Perms: []Perm{PermViewUnfrozen}}, true},
	}
	for _, c := range bad {
		entry := c.entry
		if err := entry.Validate(c.global); err == nil {
			t.Errorf("%s: ожидалась ошибка", c.name)
		}
	}

	// Логин обрезается, лишние права схлопываются, токен у парольной записи не
	// остаётся (иначе была бы вторая, забытая дверь).
	entry := AccessEntry{
		Title: "  Жюри  ", Auth: AccessAuthPassword, Login: " j ", Password: "p", Token: "остаток",
		Perms: []Perm{PermGradesManual, PermGradesManual, PermViewUnfrozen},
		Scope: AccessScopeGroups, Groups: []string{"g1"},
	}
	if err := entry.Validate(false); err != nil {
		t.Fatalf("корректная запись отклонена: %v", err)
	}
	if entry.Title != "Жюри" || entry.Login != "j" || entry.Token != "" {
		t.Fatalf("запись не приведена в порядок: %+v", entry)
	}
	if len(entry.Perms) != 2 {
		t.Fatalf("дубли прав должны схлопнуться: %v", entry.Perms)
	}
	if entry.Scope != "" || entry.Groups != nil {
		t.Fatalf("у локального доступа области быть не должно: %+v", entry)
	}
}

// Пресеты вкладываются друг в друга и узнаются по набору прав.
func TestAccessPresets(t *testing.T) {
	observer, jury, admin := NewPermSet(ObserverPerms()...), NewPermSet(JuryPerms()...), NewPermSet(AdminPerms()...)
	for p := range observer {
		if !jury.Has(p) || !admin.Has(p) {
			t.Errorf("право наблюдателя %q должно быть у жюри и админа", p)
		}
	}
	if jury.Has(PermContestsManage) || !admin.Has(PermContestsManage) {
		t.Error("управление контестами — только у админа")
	}
	if PresetIDFor(JuryPerms()) != "jury" || PresetIDFor(AdminPerms()) != "admin" {
		t.Error("пресет должен узнаваться по набору прав")
	}
	if PresetIDFor(append(JuryPerms(), PermContestsManage)) != "" {
		t.Error("набор, не совпадающий с пресетом, — свой")
	}
}

// CoversGroup: локальный доступ покрывает свою группу, глобальный — по области.
func TestAccessCoversGroup(t *testing.T) {
	local := AccessEntry{Title: "A", Auth: AccessAuthToken, Token: "t"}
	if !local.CoversGroup("любая") {
		t.Error("локальный доступ живёт в своей группе и покрывает её")
	}
	all := AccessEntry{Scope: AccessScopeAll}
	some := AccessEntry{Scope: AccessScopeGroups, Groups: []string{"g1", "g2"}}
	if !all.CoversGroup("g9") || !some.CoversGroup("g2") || some.CoversGroup("g9") {
		t.Errorf("область действия работает неверно: %v %v", all, some)
	}
}

// Легаси-поля читаются как доступы, пока не сохранён список accesses.
func TestEffectiveAccessesLegacy(t *testing.T) {
	var gf GroupFile
	if err := json.Unmarshal([]byte(`{"group_secret_token":"tok",
		"panel_access":{"jury":{"login":"j","password":"jp"},"admin":{"login":"a","password":"ap"}}}`), &gf); err != nil {
		t.Fatal(err)
	}
	list := gf.EffectiveAccesses()
	if len(list) != 3 {
		t.Fatalf("ожидались три виртуальных доступа: %+v", list)
	}
	for _, e := range list {
		if !e.Legacy || !e.IsEnabled() {
			t.Errorf("виртуальный доступ должен быть включён и помечен: %+v", e)
		}
	}
	if !list[0].UsesToken() || list[0].PermSet().Has(PermGradesManual) {
		t.Errorf("старый токен = наблюдатель: %+v", list[0])
	}
	if !list[2].PermSet().Has(PermContestsManage) {
		t.Errorf("старая учётка админа = админ группы: %+v", list[2])
	}

	// Есть свой список — легаси больше не читается.
	gf.Accesses = []AccessEntry{{ID: "x", Title: "X", Auth: AccessAuthToken, Token: "new", Perms: []Perm{PermViewUnfrozen}}}
	if list := gf.EffectiveAccesses(); len(list) != 1 || list[0].Token != "new" {
		t.Fatalf("свой список вытесняет легаси: %+v", list)
	}
}

// Enabled: отсутствующее поле — «включён», явный false — выключен и не пишется
// лишний раз в JSON.
func TestAccessEnabledRoundTrip(t *testing.T) {
	var e AccessEntry
	if err := json.Unmarshal([]byte(`{"id":"a","title":"A","auth":"token","token":"t","perms":["view.unfrozen"]}`), &e); err != nil {
		t.Fatal(err)
	}
	if !e.IsEnabled() {
		t.Error("без поля enabled доступ считается включённым")
	}
	off := false
	e.Enabled = &off
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"enabled":false`) {
		t.Fatalf("выключенность должна сохраняться: %s", blob)
	}
}

// Каждое право из пресетов должно быть в каталоге: иначе форма его не покажет,
// а сохранение доступа с этим пресетом упрётся в «неизвестное право».
func TestPresetPermsAreKnown(t *testing.T) {
	for _, preset := range Presets() {
		for _, p := range preset.Perms {
			if !KnownPerm(p) {
				t.Errorf("пресет %q: право %q нет в каталоге", preset.ID, p)
			}
		}
		entry := AccessEntry{
			Title: preset.Title, Auth: AccessAuthToken, Token: "t",
			Scope: AccessScopeAll, Perms: preset.Perms,
		}
		if err := entry.Validate(true); err != nil {
			t.Errorf("пресет %q не проходит проверку: %v", preset.ID, err)
		}
	}
	// Локальные пресеты не должны требовать глобальных прав.
	for _, preset := range Presets() {
		if preset.GlobalOnly {
			continue
		}
		entry := AccessEntry{Title: preset.Title, Auth: AccessAuthToken, Token: "t", Perms: preset.Perms}
		if err := entry.Validate(false); err != nil {
			t.Errorf("пресет %q не годится для группы: %v", preset.ID, err)
		}
	}
}
