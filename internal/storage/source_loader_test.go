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
