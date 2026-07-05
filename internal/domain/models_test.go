package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Разбор параметра заморозки и расчёт момента заморозки от окна.
func TestParseFreezeSpecAndMoment(t *testing.T) {
	if spec, err := ParseFreezeSpec(""); err != nil || spec != nil {
		t.Fatalf("empty must be nil: %v %v", spec, err)
	}
	if _, err := ParseFreezeSpec("вчера"); err == nil {
		t.Fatal("invalid duration must fail")
	}
	if _, err := ParseFreezeSpec("-1h"); err == nil {
		t.Fatal("negative duration must fail")
	}
	if _, err := ParseFreezeSpec("0s"); err == nil {
		t.Fatal("zero duration must fail")
	}

	start := time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 1, 19, 0, 0, 0, time.UTC)

	spec, err := ParseFreezeSpec("1h")
	if err != nil {
		t.Fatalf("1h: %v", err)
	}
	m := spec.FreezeMoment(&start, &end)
	if m == nil || !m.Equal(end.Add(-time.Hour)) {
		t.Fatalf("1h freeze moment wrong: %v", m)
	}

	// Длительность больше окна — прижимается к началу.
	spec, _ = ParseFreezeSpec("10h")
	if m = spec.FreezeMoment(&start, &end); m == nil || !m.Equal(start) {
		t.Fatalf("over-long freeze must clamp to start: %v", m)
	}

	// all — с самого начала.
	spec, _ = ParseFreezeSpec("ALL")
	if m = spec.FreezeMoment(&start, &end); m == nil || !m.Equal(start) {
		t.Fatalf("all freeze must equal start: %v", m)
	}

	// Без полного окна заморозке не от чего отсчитываться.
	if spec.FreezeMoment(&start, nil) != nil || spec.FreezeMoment(nil, &end) != nil {
		t.Fatal("freeze without full window must be nil")
	}
	var none *FreezeSpec
	if none.FreezeMoment(&start, &end) != nil {
		t.Fatal("nil spec must give nil moment")
	}
}

// Round-trip group.json с настроенными grades: перезапись файла (intake,
// админка) не должна менять формат полей. Регрессия: NormalizeSpec без
// MarshalJSON превращал "normalize": "max" в объект, и группа ломалась.
func TestGroupFileRoundTripKeepsGradesFormat(t *testing.T) {
	src := `{
  "title": "П3 - Искатели",
  "form_link": "https://forms.yandex.ru/u/abc",
  "update": true,
  "student_ids": ["voron-ea", "kaleev-ve"],
  "grades": {
    "title": "Оценки",
    "round": 1,
    "columns": [
      {"id": "educational", "title": "Тематические", "weight": 0.35, "type": "table", "table_name": "Тематические", "metric": "plus", "normalize": "max"},
      {"id": "olymp", "title": "Соревнования", "weight": 0.35, "type": "table", "table_name": "Соревнования", "metric": "score", "normalize": "total"},
      {"id": "fixed", "title": "Фикс", "weight": 0.2, "type": "table", "metric": "score", "normalize": 12.5},
      {"id": "zachet", "title": "Зачет", "weight": 0.3, "type": "manual"}
    ]
  }
}`

	var gf GroupFile
	if err := json.Unmarshal([]byte(src), &gf); err != nil {
		t.Fatalf("unmarshal source: %v", err)
	}

	blob, err := json.Marshal(gf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Главное: перезаписанный файл читается снова (раньше падало на normalize).
	var again GroupFile
	if err := json.Unmarshal(blob, &again); err != nil {
		t.Fatalf("re-unmarshal written group.json: %v; blob=%s", err, blob)
	}

	if again.Grades == nil || len(again.Grades.Columns) != 4 {
		t.Fatalf("grades lost: %s", blob)
	}
	if again.Grades.Round == nil || *again.Grades.Round != 1 {
		t.Fatalf("round lost: %s", blob)
	}
	cols := again.Grades.Columns
	if cols[0].Normalize.Mode != NormalizeMax {
		t.Fatalf("normalize max lost: %+v", cols[0])
	}
	if cols[1].Normalize.Mode != NormalizeTotal {
		t.Fatalf("normalize total lost: %+v", cols[1])
	}
	if cols[2].Normalize.Mode != NormalizeFixed || cols[2].Normalize.Value != 12.5 {
		t.Fatalf("normalize fixed lost: %+v", cols[2])
	}
	if again.FormLink != gf.FormLink || again.Title != gf.Title || len(again.StudentIDs) != 2 {
		t.Fatalf("group fields lost: %s", blob)
	}

	// Формат в файле остаётся строкой/числом, а не объектом.
	s := string(blob)
	if !strings.Contains(s, `"normalize":"max"`) || !strings.Contains(s, `"normalize":"total"`) || !strings.Contains(s, `"normalize":12.5`) {
		t.Fatalf("normalize written in wrong format: %s", s)
	}
	if strings.Contains(s, `"Mode"`) {
		t.Fatalf("normalize serialized as struct: %s", s)
	}
}
