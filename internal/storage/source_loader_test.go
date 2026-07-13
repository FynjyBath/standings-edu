package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

// Проверяем, что генератор понимает записи в том виде, в котором их пишет
// админка (add-ref, set-options, inline-save) — совместимость двух модулей.
func TestParseGroupContestItemAdminFormats(t *testing.T) {
	t.Run("ref from add-ref", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","update":true}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.ID != "c1" || !ref.Update || ref.Inline != nil {
			t.Fatalf("unexpected ref: %+v", ref)
		}
	})

	t.Run("ref with string table_name", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","update":false,"table_name":"Одна"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Update || len(ref.TableNames) != 1 || ref.TableNames[0] != "Одна" || ref.Inline != nil {
			t.Fatalf("unexpected ref: %+v", ref)
		}
	})

	t.Run("ref with group-side start/end window", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","update":true,"start_time":"2026-09-01T18:00:00+03:00","end_time":"2026-09-01T20:00:00+03:00"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Inline != nil {
			t.Fatalf("start/end must not make entry inline: %+v", ref)
		}
		if ref.StartTime == nil || ref.EndTime == nil {
			t.Fatalf("window not parsed: %+v", ref)
		}
		if ref.StartTime.UTC().Hour() != 15 {
			t.Fatalf("unexpected start time: %v", ref.StartTime)
		}
	})

	t.Run("ref with freeze", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","update":true,"freeze":"1h"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Inline != nil || ref.Freeze == nil || ref.Freeze.All || ref.Freeze.Duration.String() != "1h0m0s" {
			t.Fatalf("freeze not parsed: %+v", ref.Freeze)
		}

		ref, err = parseGroupContestItem(json.RawMessage(`{"id":"c1","freeze":"all"}`))
		if err != nil || ref.Freeze == nil || !ref.Freeze.All {
			t.Fatalf("freeze all not parsed: %+v %v", ref.Freeze, err)
		}

		if _, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","freeze":"скоро"}`)); err == nil {
			t.Fatal("invalid freeze must fail")
		}
	})

	t.Run("inline with entry-level freeze", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"inl","title":"И","score_system":"edu","subcontests":[],"update":false,"freeze":"30m"}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Inline == nil || ref.Freeze == nil || ref.Freeze.Duration.String() != "30m0s" {
			t.Fatalf("inline freeze not parsed: %+v", ref)
		}
	})

	t.Run("ref with list table_name", func(t *testing.T) {
		ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","update":true,"table_name":["A","B"]}`))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(ref.TableNames) != 2 || ref.Inline != nil {
			t.Fatalf("unexpected ref: %+v", ref)
		}
	})

	t.Run("inline from inline-save", func(t *testing.T) {
		raw := json.RawMessage(`{"id":"inl","title":"Инлайн","score_system":"ioi","source_type":"tasks","table_name":"Тема","subcontests":[{"title":"S","tasks":["https://acmp.ru/?id=1"]}],"update":false}`)
		ref, err := parseGroupContestItem(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Inline == nil {
			t.Fatalf("expected inline contest, got ref: %+v", ref)
		}
		if ref.Update {
			t.Fatalf("update flag must be false: %+v", ref)
		}
		if ref.Inline.Title != "Инлайн" || string(ref.Inline.ScoreSystem) != "ioi" || len(ref.Inline.Subcontests) != 1 {
			t.Fatalf("inline contest fields lost: %+v", ref.Inline)
		}
	})

	t.Run("inline provider from inline-save", func(t *testing.T) {
		raw := json.RawMessage(`{"id":"p1","score_system":"ioi","source_type":"provider","provider":"codeforces_contest","provider_config":{"contest_id":1711},"subcontests":[],"update":true}`)
		ref, err := parseGroupContestItem(raw)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Inline == nil || ref.Inline.Provider != "codeforces_contest" {
			t.Fatalf("provider inline not parsed: %+v", ref)
		}
	})
}

// Переопределения в записи группы: zero_penalty/summary_total_only/freeze
// парсятся как опциональные; невалидные значения — ошибка.
func TestParseGroupContestItemOverrides(t *testing.T) {
	ref, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","zero_penalty":0,"summary_total_only":false,"hidden":true,"freeze":"none"}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ref.ZeroPenalty == nil || *ref.ZeroPenalty != 0 {
		t.Fatalf("zero_penalty=0 must be explicit disable: %+v", ref.ZeroPenalty)
	}
	if ref.SummaryTotalOnly == nil || *ref.SummaryTotalOnly {
		t.Fatalf("summary_total_only=false must be explicit: %+v", ref.SummaryTotalOnly)
	}
	if ref.Hidden == nil || !*ref.Hidden {
		t.Fatalf("hidden=true must be explicit: %+v", ref.Hidden)
	}
	if ref.Freeze == nil || !ref.Freeze.None {
		t.Fatalf("freeze none must parse: %+v", ref.Freeze)
	}

	ref, err = parseGroupContestItem(json.RawMessage(`{"id":"c1"}`))
	if err != nil || ref.ZeroPenalty != nil || ref.SummaryTotalOnly != nil || ref.Hidden != nil || ref.Freeze != nil {
		t.Fatalf("absent overrides must be nil: %+v %v", ref, err)
	}

	if _, err := parseGroupContestItem(json.RawMessage(`{"id":"c1","zero_penalty":-2}`)); err == nil {
		t.Fatal("negative zero_penalty must fail")
	}
}

// Регресс: объединённая группа (member_groups, без своего contests.json) не должна
// ронять загрузку исходных данных. Раньше отсутствие contests.json было ошибкой.
func TestLoadCombinedGroupNoContestsFile(t *testing.T) {
	dir := t.TempDir()
	writeSourceFile(t, filepath.Join(dir, "students.json"), `[]`)
	writeSourceFile(t, filepath.Join(dir, "contests.json"), `[]`)
	// обычная группа со своим contests.json
	writeSourceFile(t, filepath.Join(dir, "groups", "grp_a", "group.json"), `{"title":"A","student_ids":["s1"]}`)
	writeSourceFile(t, filepath.Join(dir, "groups", "grp_a", "contests.json"), `[]`)
	// объединённая группа — только group.json, contests.json НЕТ
	writeSourceFile(t, filepath.Join(dir, "groups", "combo", "group.json"), `{"title":"Combo","member_groups":["grp_a"]}`)

	data, err := NewSourceLoader(dir).Load()
	if err != nil {
		t.Fatalf("Load must not fail on combined group without contests.json: %v", err)
	}
	var combo *domain.GroupDefinition
	for i := range data.Groups {
		if data.Groups[i].Slug == "combo" {
			combo = &data.Groups[i]
		}
	}
	if combo == nil {
		t.Fatal("combo group not loaded")
	}
	if len(combo.MemberGroups) != 1 || combo.MemberGroups[0] != "grp_a" {
		t.Fatalf("member_groups wrong: %+v", combo.MemberGroups)
	}
	if len(combo.Contests) != 0 {
		t.Fatalf("combined group must have no contests: %+v", combo.Contests)
	}
}

func writeSourceFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Таблицы кондуитов из manual_tables.json подставляются в provider_config при
// загрузке: глобальные — из data/manual_tables.json, inline — из файла группы.
// Запись в файле приоритетнее легаси-таблицы в конфиге.
func TestLoadInjectsManualTables(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(rel, v string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("students.json", `[{"id":"s1","full_name":"Иванов Иван"}]`)
	mustWrite("contests.json", `[{"id":"gk","title":"К","score_system":"edu","source_type":"provider",
		"provider":"manual_table","provider_config":{"task_count":1,"table":"legacy\t1\n"},"subcontests":[]}]`)
	mustWrite("manual_tables.json", `{"gk":"ФИО\t1\nИванов Иван\t1\n"}`)
	mustWrite("groups/g1/group.json", `{"title":"Г","student_ids":["s1"]}`)
	mustWrite("groups/g1/contests.json", `[{"id":"ik","title":"Инлайн","score_system":"edu","source_type":"provider",
		"provider":"manual_table","provider_config":{"task_count":1},"subcontests":[],"update":true}]`)
	mustWrite("groups/g1/manual_tables.json", `{"ik":"ФИО\t1\nИванов Иван\t+\n"}`)

	data, err := NewSourceLoader(dir).Load()
	if err != nil {
		t.Fatal(err)
	}
	// Глобальный: таблица из файла (перекрывает легаси).
	var cfg map[string]any
	if err := json.Unmarshal(data.Contests["gk"].ProviderConfig, &cfg); err != nil {
		t.Fatal(err)
	}
	if table, _ := cfg["table"].(string); !strings.Contains(table, "Иванов Иван") || strings.Contains(table, "legacy") {
		t.Fatalf("global table not injected: %q", cfg["table"])
	}
	// Инлайн группы.
	inline := data.Groups[0].Contests[0].Inline
	if inline == nil {
		t.Fatal("inline contest expected")
	}
	if err := json.Unmarshal(inline.ProviderConfig, &cfg); err != nil {
		t.Fatal(err)
	}
	if table, _ := cfg["table"].(string); !strings.Contains(table, "+") {
		t.Fatalf("inline table not injected: %q", cfg["table"])
	}
}
