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

		rawByStudent, reference := computeTableColumn(col, standings, roster)
		resolvers = append(resolvers, func(studentID string) (float64, bool) {
			if reference <= 0 {
				return 0, true
			}
			return clamp(rawByStudent[studentID]/reference*10, 0, 10), true
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
			rounded := roundTo(value, decimals)
			values[i] = &rounded
			weightedSum += value * weights[i]
			weightTotal += weights[i]
		}
		var final *float64
		if weightTotal > 0 {
			f := roundTo(weightedSum/weightTotal, decimals)
			final = &f
		}
		rows = append(rows, domain.GeneratedGradeRow{
			StudentID:  student.ID,
			PublicName: student.PublicName,
			Values:     values,
			Final:      final,
		})
	}

	// Сортировка по убыванию итога; без итога — вниз; при равенстве — по имени.
	sort.SliceStable(rows, func(i, j int) bool {
		fi, fj := rows[i].Final, rows[j].Final
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

	return &domain.GeneratedGrades{
		Title:   strings.TrimSpace(cfg.Title),
		Columns: columns,
		Rows:    rows,
	}
}

// computeTableColumn возвращает сырое значение метрики по каждому ученику и
// значение нормировки (reference).
func computeTableColumn(col domain.GradeColumn, standings domain.GeneratedGroupStandings, roster []RosterStudent) (map[string]float64, float64) {
	useScore := strings.EqualFold(col.Metric, domain.GradeMetricScore)
	wantTable := strings.TrimSpace(col.TableName)

	rawByStudent := make(map[string]float64)
	taskCount := 0

	for _, contest := range standings.Contests {
		if wantTable != "" && !containsTableName(contest.TableNames, wantTable) {
			continue
		}
		taskCount += len(contest.Tasks)
		for _, row := range contest.Rows {
			if useScore {
				rawByStudent[row.StudentID] += float64(row.TotalScore)
			} else {
				rawByStudent[row.StudentID] += float64(row.SolvedCount)
			}
		}
	}

	reference := 0.0
	switch col.Normalize.Mode {
	case domain.NormalizeFixed:
		reference = col.Normalize.Value
	case domain.NormalizeTotal:
		if useScore {
			reference = float64(taskCount) * 100
		} else {
			reference = float64(taskCount)
		}
	default: // max (в т.ч. пустой режим)
		for _, student := range roster {
			if r := rawByStudent[student.ID]; r > reference {
				reference = r
			}
		}
	}

	return rawByStudent, reference
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
