// Package grades вычисляет таблицу оценок группы на этапе генерации.
package grades

import (
	"math"
	"sort"
	"strings"

	"standings-edu/internal/domain"
)

// RosterStudent — ученик группы для строки таблицы оценок.
type RosterStudent struct {
	ID         string
	PublicName string
}

// Build считает таблицу оценок. Возвращает nil, если конфиг пуст.
//   - cfg — конфиг из group.json;
//   - standings — уже собранные (в т.ч. слитые) standings группы (для табличных оценок);
//   - roster — ученики группы по порядку student_ids;
//   - manual — ручные оценки: columnID -> studentID -> значение (0..10).
func Build(cfg *domain.GradesConfig, standings domain.GeneratedGroupStandings, roster []RosterStudent, manual map[string]map[string]float64) *domain.GeneratedGrades {
	if cfg == nil || len(cfg.Columns) == 0 || len(roster) == 0 {
		return nil
	}

	decimals := 1
	if cfg.Round != nil && *cfg.Round >= 0 {
		decimals = *cfg.Round
	}

	columns := make([]domain.GeneratedGradeColumn, 0, len(cfg.Columns))
	weights := make([]float64, 0, len(cfg.Columns))
	// resolver возвращает (значение 0..10, есть ли оценка) для ученика.
	resolvers := make([]func(studentID string) (float64, bool), 0, len(cfg.Columns))

	for _, col := range cfg.Columns {
		weight := col.Weight
		if weight <= 0 {
			weight = 1
		}
		columns = append(columns, domain.GeneratedGradeColumn{Title: col.Title, Weight: weight})
		weights = append(weights, weight)

		if strings.EqualFold(col.Type, domain.GradeColumnManual) {
			colID := col.ID
			resolvers = append(resolvers, func(studentID string) (float64, bool) {
				byStudent, ok := manual[colID]
				if !ok {
					return 0, false
				}
				value, ok := byStudent[studentID]
				if !ok {
					return 0, false
				}
				return clamp(value, 0, 10), true
			})
			continue
		}

		rawByStudent, refByStudent, applies := computeTableColumn(col, standings, roster)
		resolvers = append(resolvers, func(studentID string) (float64, bool) {
			if !applies[studentID] {
				// Режим «не учитывать пропущенные»: ученик не участвовал ни в
				// одном контесте столбца — оценки по столбцу у него нет (в
				// среднее не идёт), а не ноль.
				return 0, false
			}
			ref := refByStudent[studentID]
			if ref <= 0 {
				return 0, true
			}
			return clamp(rawByStudent[studentID]/ref*10, 0, 10), true
		})
	}

	rows := make([]domain.GeneratedGradeRow, 0, len(roster))
	for _, student := range roster {
		values := make([]*float64, len(cfg.Columns))
		weightedSum := 0.0
		weightTotal := 0.0
		for i := range cfg.Columns {
			value, ok := resolvers[i](student.ID)
			if !ok {
				continue
			}
			// Значения столбцов храним реальными (round применяется только к
			// итогу): 4 знаков достаточно для любого показа, JSON не пухнет от
			// хвостов вида 3.966666666667.
			rounded := roundTo(value, 4)
			values[i] = &rounded
			weightedSum += value * weights[i]
			weightTotal += weights[i]
		}
		var final, finalRaw *float64
		if weightTotal > 0 {
			raw := weightedSum / weightTotal
			f := roundTo(raw, decimals)
			final = &f
			finalRaw = &raw
		}
		rows = append(rows, domain.GeneratedGradeRow{
			StudentID:  student.ID,
			PublicName: student.PublicName,
			Values:     values,
			Final:      final,
			FinalRaw:   finalRaw,
		})
	}

	// Сортировка по убыванию ТОЧНОГО итога (при round=0 округлённые итоги
	// слипаются в группы, а ранжировать всё равно надо честно); без итога —
	// вниз; при равенстве — по имени.
	sort.SliceStable(rows, func(i, j int) bool {
		fi, fj := rows[i].FinalRaw, rows[j].FinalRaw
		switch {
		case fi != nil && fj == nil:
			return true
		case fi == nil && fj != nil:
			return false
		case fi != nil && fj != nil && *fi != *fj:
			return *fi > *fj
		}
		return strings.ToLower(rows[i].PublicName) < strings.ToLower(rows[j].PublicName)
	})

	// Показ: столбцы и «точно» — с одинаковым числом знаков. round управляет
	// округлением итога; при round=0 (целый итог) реальные значения столбцов
	// показываем с двумя знаками, а не целыми.
	display := decimals
	if display < 1 {
		display = 2
	}
	return &domain.GeneratedGrades{
		Title:    strings.TrimSpace(cfg.Title),
		Columns:  columns,
		Rows:     rows,
		Decimals: &display,
	}
}

// computeTableColumn возвращает по каждому ученику: сырое значение метрики
// (rawByStudent), знаменатель нормировки (refByStudent) и признак, что столбец
// к ученику применим (applies).
//
// Обычный режим: знаменатель одинаков для всех (как раньше), applies=true у
// всех. Режим col.IgnoreMissingContests: у каждого ученика знаменатель
// складывается только из контестов, где он что-то делал (были попытки или
// баллы); контесты, целиком пропущенные, в оценку не входят. Если ученик
// пропустил все контесты столбца, applies=false — оценки по столбцу нет.
func computeTableColumn(col domain.GradeColumn, standings domain.GeneratedGroupStandings, roster []RosterStudent) (rawByStudent, refByStudent map[string]float64, applies map[string]bool) {
	useScore := strings.EqualFold(col.Metric, domain.GradeMetricScore)
	wantTable := strings.TrimSpace(col.TableName)
	ignoreMissing := col.IgnoreMissingContests

	rawByStudent = make(map[string]float64)
	refByStudent = make(map[string]float64)
	applies = make(map[string]bool)

	// Поконтестные данные: число задач, поконтестный максимум метрики и кто в
	// контесте участвовал (для режима «не учитывать пропущенные»).
	type contestData struct {
		taskCount    int
		contestMax   float64
		participated map[string]bool
	}
	var chosen []contestData
	totalTaskCount := 0

	for _, contest := range standings.Contests {
		if wantTable != "" && !containsTableName(contest.TableNames, wantTable) {
			continue
		}
		cd := contestData{taskCount: len(contest.Tasks), participated: make(map[string]bool)}
		for _, row := range contest.Rows {
			value := contestRowValue(col, contest, row, useScore)
			rawByStudent[row.StudentID] += value
			if value > cd.contestMax {
				cd.contestMax = value
			}
			if ignoreMissing && participatedInContest(row) {
				cd.participated[row.StudentID] = true
			}
		}
		chosen = append(chosen, cd)
		totalTaskCount += cd.taskCount
	}

	// refWeight — вклад одного контеста в знаменатель по режиму нормировки.
	refWeight := func(cd contestData) float64 {
		switch col.Normalize.Mode {
		case domain.NormalizeFixed:
			// Фикс. число распределяем между контестами пропорционально числу
			// задач: доля контеста = value·taskCount/allTasks. Сумма долей по
			// всем контестам = value (прежний общий знаменатель).
			if totalTaskCount > 0 {
				return col.Normalize.Value * float64(cd.taskCount) / float64(totalTaskCount)
			}
			return 0
		case domain.NormalizeTotal:
			if useScore {
				return float64(cd.taskCount) * 100
			}
			return float64(cd.taskCount)
		default: // max (в т.ч. пустой режим): поконтестный максимум
			return cd.contestMax
		}
	}

	if !ignoreMissing {
		// Прежнее поведение: общий знаменатель для всех, применяется ко всем.
		reference := 0.0
		if col.Normalize.Mode == domain.NormalizeFixed {
			reference = col.Normalize.Value // без деления на задачи — как раньше
		} else {
			for _, cd := range chosen {
				reference += refWeight(cd)
			}
		}
		for _, student := range roster {
			refByStudent[student.ID] = reference
			applies[student.ID] = true
		}
		return rawByStudent, refByStudent, applies
	}

	// Режим «не учитывать пропущенные»: знаменатель по каждому ученику из тех
	// контестов, где он участвовал.
	for _, cd := range chosen {
		w := refWeight(cd)
		for sid := range cd.participated {
			refByStudent[sid] += w
			applies[sid] = true
		}
	}
	return rawByStudent, refByStudent, applies
}

// participatedInContest — ученик что-то делал в контесте: есть решённые/попытанные
// задачи или ненулевые баллы (в т.ч. в дорешке). Всё по нулям и без попыток —
// контест считается пропущенным.
func participatedInContest(row domain.GeneratedRow) bool {
	if row.SolvedCount != 0 || row.TotalScore != 0 {
		return true
	}
	for _, s := range row.Statuses {
		if s == domain.TaskStatusSolved || s == domain.TaskStatusAttempted {
			return true
		}
	}
	for _, sc := range row.Scores {
		if sc != nil && *sc != 0 {
			return true
		}
	}
	for _, sc := range row.PracticeScores {
		if sc != nil && *sc != 0 {
			return true
		}
	}
	return false
}

// contestRowValue — вклад одного контеста в колонку оценки для строки ученика.
// Без коэффициента дорешки (col.Upsolving == nil) — прежнее поведение: готовые
// TotalScore/SolvedCount (дорешка на полную). С коэффициентом k вклад каждой
// задачи = max(основной результат, дорешка×k); штраф контеста за нулевые
// задачи применяется и здесь, чтобы оценка сходилась с таблицей.
func contestRowValue(col domain.GradeColumn, contest domain.GeneratedContestStandings, row domain.GeneratedRow, useScore bool) float64 {
	if col.Upsolving == nil {
		if useScore {
			return float64(row.TotalScore)
		}
		return float64(row.SolvedCount)
	}

	coef := clamp(*col.Upsolving, 0, 1)
	total := 0.0
	zeros := 0
	for i := range contest.Tasks {
		contribution := 0.0
		if useScore {
			main := 0.0
			if i < len(row.Scores) && row.Scores[i] != nil {
				main = float64(*row.Scores[i])
			}
			practice := 0.0
			if i < len(row.PracticeScores) && row.PracticeScores[i] != nil {
				practice = float64(*row.PracticeScores[i])
			}
			// Основной балл + доля прироста от дорешки: a + max(0, b−a)·coef.
			// Так дорешивание всегда выгодно (в отличие от max(a, b·coef),
			// где при большом a дорешка ничего не давала).
			contribution = main + math.Max(0, practice-main)*coef
		} else {
			solved := i < len(row.Statuses) && row.Statuses[i] == domain.TaskStatusSolved
			practiceOnly := i < len(row.Upsolved) && row.Upsolved[i]
			switch {
			case solved && !practiceOnly:
				contribution = 1
			case solved && practiceOnly:
				contribution = coef
			}
		}
		total += contribution
		if contribution <= 0 {
			zeros++
		}
	}
	if useScore && contest.ZeroPenalty > 0 {
		total -= float64(zeros * contest.ZeroPenalty)
	}
	return total
}

func containsTableName(names domain.TableNameList, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func roundTo(v float64, decimals int) float64 {
	if decimals <= 0 {
		return math.Round(v)
	}
	factor := math.Pow(10, float64(decimals))
	return math.Round(v*factor) / factor
}
