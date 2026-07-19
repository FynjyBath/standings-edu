package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"

	"standings-edu/internal/domain"
)

func iptr2(v int) *int { return &v }

// Книга экспорта: листы «Все» + вкладки, лист «Оценки» с формулами COUNTIF/SUM
// по листам вкладок и весами в ячейках; порядок учеников одинаков на листах.
func TestBuildGroupExportWorkbook(t *testing.T) {
	std := domain.GeneratedGroupStandings{
		GroupSlug: "g", GroupTitle: "Группа",
		Contests: []domain.GeneratedContestStandings{
			{
				ID: "olymp1", Title: "Тренировка", ScoreSystem: domain.ScoreSystemIOI,
				TableNames: domain.TableNameList{"Соревнования"},
				Tasks:      []domain.GeneratedTask{{Label: "A"}, {Label: "B"}},
				Rows: []domain.GeneratedRow{
					{StudentID: "b", PublicName: "Борис", TotalScore: 150, Statuses: []string{"solved", "attempted"}, Scores: []*int{iptr2(100), iptr2(50)}},
					{StudentID: "a", PublicName: "Анна", TotalScore: 30, Statuses: []string{"attempted", "none"}, Scores: []*int{iptr2(30), nil}},
				},
			},
			{
				ID: "edu1", Title: "Циклы", ScoreSystem: domain.ScoreSystemEdu,
				TableNames: domain.TableNameList{"Тематические"},
				Tasks:      []domain.GeneratedTask{{Label: "A"}, {Label: "B"}, {Label: "C"}},
				Rows: []domain.GeneratedRow{
					{StudentID: "a", PublicName: "Анна", SolvedCount: 2, Statuses: []string{"solved", "solved", "attempted"}},
					{StudentID: "b", PublicName: "Борис", SolvedCount: 1, Statuses: []string{"solved", "none", "none"}},
				},
			},
			{
				ID: "edu2", Title: "Сборник строк", ScoreSystem: domain.ScoreSystemEdu,
				TableNames: domain.TableNameList{"Тематические"}, SummaryTotalOnly: true, ShortName: "стр",
				Tasks: []domain.GeneratedTask{{Label: "A"}, {Label: "B"}},
				Rows: []domain.GeneratedRow{
					{StudentID: "a", PublicName: "Анна", SolvedCount: 2, Statuses: []string{"solved", "solved"}},
				},
			},
		},
	}
	cfg := &domain.GradesConfig{
		Round: iptr2(1),
		Columns: []domain.GradeColumn{
			{ID: "edu", Title: "Тематические", Weight: 0.5, Type: "table", TableName: "Тематические", Metric: "plus", Normalize: domain.NormalizeSpec{Mode: domain.NormalizeTotal}},
			{ID: "olymp", Title: "Соревнования", Weight: 0.3, Type: "table", TableName: "Соревнования", Metric: "score", Normalize: domain.NormalizeSpec{Mode: domain.NormalizeMax}},
			{ID: "zachet", Title: "Зачёт", Weight: 0.2, Type: "manual"},
		},
	}
	manual := map[string]map[string]float64{"zachet": {"a": 7.5}}

	wb := buildGroupExportWorkbook(std, cfg, manual)

	var buf bytes.Buffer
	if err := wb.Write(&buf); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("книга должна быть валидным zip: %v", err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(body)
	}

	// Листы: Все, Соревнования, Тематические, Оценки.
	book := files["xl/workbook.xml"]
	for _, name := range []string{"Все", "Соревнования", "Тематические", "Оценки"} {
		if !strings.Contains(book, `name="`+name+`"`) {
			t.Fatalf("нет листа %q: %s", name, book)
		}
	}
	if !strings.Contains(book, `fullCalcOnLoad="1"`) {
		t.Fatal("нужен fullCalcOnLoad для пересчёта формул")
	}

	// Лист «Тематические» (3-й: Все=1, Соревнования=2): плюсы и колонка-сумма.
	tem := files["xl/worksheets/sheet3.xml"]
	if !strings.Contains(tem, `<t xml:space="preserve">+</t>`) {
		t.Fatal("на листе должны быть «+»")
	}
	if !strings.Contains(tem, "COUNTIF(D3:G3,&#34;+&#34;)+N(G3)") {
		t.Fatalf("Σ+ должен считаться формулой с колонкой-суммой: %s", tem)
	}

	// «Оценки» (4-й лист): веса в ячейках, формулы по листам вкладок, итог.
	gr := files["xl/worksheets/sheet4.xml"]
	for _, want := range []string{
		"<v>0.5</v>", "<v>0.3</v>", "<v>0.2</v>", // веса
		"COUNTIF(&#39;Тематические&#39;!D3:&#39;Тематические&#39;!F3,&#34;+&#34;)", // плюсы вкладки
		"N(&#39;Тематические&#39;!G3)",     // колонка-сумма вкладки
		"SUM(&#39;Соревнования&#39;!D3:",   // баллы вкладки
		"MAX(D$3:D$4)", // normalize=max по сырой колонке «Соревнования»
		"B3/5*10",      // normalize=total: 5 задач вкладки «Тематические»
		"<v>7.5</v>",                       // ручная оценка Анны
		"IFERROR(ROUND((",                  // итог — взвешенное среднее
	} {
		if !strings.Contains(gr, want) {
			t.Fatalf("на листе «Оценки» нет %q:\n%s", want, gr)
		}
	}

	// Порядок учеников одинаков на всех листах: Анна раньше Бориса.
	for _, sheet := range []string{files["xl/worksheets/sheet2.xml"], tem, gr} {
		ai, bi := strings.Index(sheet, "Анна"), strings.Index(sheet, "Борис")
		if ai < 0 || bi < 0 || ai > bi {
			t.Fatalf("порядок учеников должен быть одинаковым (Анна, Борис): a=%d b=%d", ai, bi)
		}
	}

	// ioi-ячейки — числами (баллы), включая эффективный максимум.
	sor := files["xl/worksheets/sheet2.xml"]
	if !strings.Contains(sor, "<v>100</v>") || !strings.Contains(sor, "<v>50</v>") {
		t.Fatalf("баллы ioi должны быть числами: %s", sor)
	}

	// Подсветка: edu «+»/«−» — текстовые правила; ioi — шкала 0..100;
	// колонка-сумма — шкала 0..макс задач с форматом «N / макс».
	if !strings.Contains(tem, `operator="equal"`) || !strings.Contains(tem, `<formula>"+"</formula>`) {
		t.Fatalf("на edu-листе должны быть правила для «+»: %s", tem)
	}
	if !strings.Contains(tem, `type="colorScale"`) {
		t.Fatal("колонка-сумма должна иметь цветовую шкалу")
	}
	if !strings.Contains(sor, `<cfvo type="num" val="0"/><cfvo type="num" val="100"/>`) {
		t.Fatalf("ioi-лист должен иметь шкалу 0..100: %s", sor)
	}
	styles := files["xl/styles.xml"]
	if !strings.Contains(styles, `formatCode="0&#34; / 2&#34;"`) {
		t.Fatalf("нужен формат «N / 2» для колонки-суммы: %s", styles)
	}
	if !strings.Contains(styles, `FF92D8AA`) || !strings.Contains(styles, `FFF8D7D7`) {
		t.Fatal("в стилях должны быть цвета solved/attempted")
	}
	if !strings.Contains(tem, `9EE19E`) {
		t.Fatal("шкала колонки-суммы должна использовать палитру сайта")
	}
}
