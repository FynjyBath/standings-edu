package storage

import (
	"encoding/json"
	"testing"
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
