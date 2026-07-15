package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"standings-edu/internal/domain"
	"standings-edu/internal/web"
)

// Имена учеников в таблицах группы кликабельны (ведут на профиль) только при
// валидном токене преподавателя; ученикам (без токена) — обычный текст.
func TestGroupStandingsClickableNames(t *testing.T) {
	renderer := web.NewTemplateRenderer("../../web/templates")

	page := func(tokenValid bool) GroupPageData {
		return GroupPageData{
			PageTitle:  "t",
			Footer:     FooterInfo{},
			Token:      "T0K",
			TokenValid: tokenValid,
			Standings: domain.GeneratedGroupStandings{
				GroupSlug:          "alpha",
				GroupTitle:         "Альфа",
				SolvedSummarySites: []string{"cf"},
				SolvedSummary: []domain.GeneratedGroupSolvedSummaryRow{
					{StudentID: "stud1", PublicName: "Иванов И.", SolvedCountOnPageSites: 1, SolvedCountBySite: []int{1}},
				},
				Contests: []domain.GeneratedContestStandings{{
					ID:          "c1",
					Title:       "Контест",
					ScoreSystem: domain.ScoreSystemEdu,
					Rows: []domain.GeneratedRow{
						{StudentID: "stud1", PublicName: "Иванов И.", Place: "1", SolvedCount: 1, Statuses: []string{}},
					},
				}},
			},
		}
	}

	render := func(d GroupPageData) string {
		rec := httptest.NewRecorder()
		if err := renderer.Render(rec, 200, "group_standings.html", d); err != nil {
			t.Fatalf("render: %v", err)
		}
		return rec.Body.String()
	}

	link := `href="/standings/alpha/student?id=stud1&token=T0K"`

	withTok := render(page(true))
	// Ссылка должна быть и в основной таблице, и в доске «решено» — минимум дважды.
	if n := strings.Count(withTok, link); n < 2 {
		t.Fatalf("под токеном имена должны быть ссылками (найдено %d, ждём ≥2): %q", n, link)
	}

	noTok := render(page(false))
	if strings.Contains(noTok, link) {
		t.Fatal("без токена имена не должны вести на профиль")
	}
	if !strings.Contains(noTok, "Иванов И.") {
		t.Fatal("имя должно остаться обычным текстом")
	}
}
