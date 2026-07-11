package domain

import (
	"encoding/json"
	"testing"
)

// Round-trip GeneratedContestStandings: у него кастомный UnmarshalJSON с теневой
// структурой — новые поля легко забыть в него добавить (как было с source_url).
// Тест ловит рассинхрон marshal↔unmarshal по полям, важным для сервера.
func TestGeneratedContestRoundTrip(t *testing.T) {
	in := GeneratedContestStandings{
		ID:               "c1",
		Title:            "T",
		ScoreSystem:      ScoreSystemEdu,
		SummaryTotalOnly: true,
		ShortName:        "SN",
		SourceURL:        "https://informatics.mccme.ru/mod/statements/view.php?id=52798",
		Tasks:            []GeneratedTask{{Label: "A", URL: "u"}},
		Rows:             []GeneratedRow{},
		Subcontests:      []GeneratedSubcontest{},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out GeneratedContestStandings
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.SourceURL != in.SourceURL {
		t.Fatalf("source_url потерялся в round-trip: %q", out.SourceURL)
	}
	if out.ShortName != in.ShortName || !out.SummaryTotalOnly || out.ID != in.ID {
		t.Fatalf("другие поля потерялись: %+v", out)
	}
}
