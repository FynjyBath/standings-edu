package standings

import (
	"math"
	"sort"
)

// Подгонка двух латентных моделей, на которых стоит цена задачи.
//
// Прежняя модель мерила «активное время на задаче» — промежутки между
// посылками. Но промежуток между посылками это время ОТЛАДКИ: когда человек
// думает час и сдаёт с первой попытки, модель видит только надбавку δ0. Так
// происходит с половиной решений, поэтому цена вырождалась в «сколько раз по
// задаче промахнулись». Здесь считаются две величины, которые в данных
// действительно наблюдаются: исход попытки и число попыток.

// fitObs — наблюдение для двусторонней модели: val = row + col + шум.
type fitObs struct {
	row, col string
	val      float64
}

// twoWayMedianFit раскладывает наблюдения на вклад строки (ученик) и столбца
// (задача) чередованием медиан. Медианы вместо средних — чтобы один
// застрявший не перекашивал оценку задачи. Результат центрируется так, что
// медианный ученик равен нулю: тогда col — величина для типичного ученика.
func twoWayMedianFit(obs []fitObs, iters int) (rows, cols map[string]float64) {
	rows, cols = map[string]float64{}, map[string]float64{}
	if len(obs) == 0 {
		return rows, cols
	}
	byRow := map[string][]int{}
	byCol := map[string][]int{}
	for i, o := range obs {
		byRow[o.row] = append(byRow[o.row], i)
		byCol[o.col] = append(byCol[o.col], i)
		rows[o.row], cols[o.col] = 0, 0
	}
	buf := make([]float64, 0, len(obs))
	for it := 0; it < iters; it++ {
		for c, idx := range byCol {
			buf = buf[:0]
			for _, i := range idx {
				buf = append(buf, obs[i].val-rows[obs[i].row])
			}
			cols[c] = median(buf)
		}
		for r, idx := range byRow {
			buf = buf[:0]
			for _, i := range idx {
				buf = append(buf, obs[i].val-cols[obs[i].col])
			}
			rows[r] = median(buf)
		}
		buf = buf[:0]
		for _, v := range rows {
			buf = append(buf, v)
		}
		shift := median(buf)
		for r := range rows {
			rows[r] -= shift
		}
		for c := range cols {
			cols[c] += shift
		}
	}
	return rows, cols
}

// binObs — наблюдение для модели Раша: получилось или нет.
type binObs struct {
	row, col string
	ok       bool
}

// raschFit подгоняет P(ok) = sigma(theta_row - diff_col) покоординатным
// Ньютоном. Регуляризация lambda обязательна: без неё задача, которую решили
// все, уводит diff в минус бесконечность.
//
// Модель отвечает на вопрос «какая доля способных до неё добраться её берёт»,
// то есть меряет порог понимания, а не возню с отладкой. Она же отделяет силу
// ученика от сложности задачи: если задачу взяли только сильные, это видно по
// их theta, а не выдаётся за лёгкость.
func raschFit(obs []binObs, iters int, lambda float64) (theta, diff map[string]float64) {
	theta, diff = map[string]float64{}, map[string]float64{}
	if len(obs) == 0 {
		return theta, diff
	}
	byRow := map[string][]int{}
	byCol := map[string][]int{}
	for i, o := range obs {
		byRow[o.row] = append(byRow[o.row], i)
		byCol[o.col] = append(byCol[o.col], i)
		theta[o.row], diff[o.col] = 0, 0
	}
	rowIDs := sortedKeys(byRow)
	colIDs := sortedKeys(byCol)
	step := func(idx []int, self float64, forRow bool) float64 {
		g, h := 0.0, 0.0
		for _, i := range idx {
			o := obs[i]
			var z float64
			if forRow {
				z = self - diff[o.col]
			} else {
				z = theta[o.row] - self
			}
			p := 1 / (1 + math.Exp(-z))
			y := 0.0
			if o.ok {
				y = 1
			}
			if forRow {
				g += y - p
			} else {
				g += p - y
			}
			h += p * (1 - p)
		}
		g -= lambda * self
		h += lambda
		if h < 1e-9 {
			return self
		}
		// Шаг ограничен: на вырожденных строках Ньютон иначе улетает.
		d := g / h
		return self + math.Max(-1, math.Min(1, d))
	}
	for it := 0; it < iters; it++ {
		for _, r := range rowIDs {
			theta[r] = step(byRow[r], theta[r], true)
		}
		for _, c := range colIDs {
			diff[c] = step(byCol[c], diff[c], false)
		}
	}
	return theta, diff
}

func sortedKeys(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// robustZ приводит величину к безразмерной шкале через медиану и медианное
// абсолютное отклонение — чтобы две разнородные оси (порог и трудоёмкость)
// можно было складывать, и чтобы хвосты не задавали масштаб.
func robustZ(v map[string]float64) map[string]float64 {
	xs := make([]float64, 0, len(v))
	for _, x := range v {
		xs = append(xs, x)
	}
	m := median(xs)
	dev := make([]float64, 0, len(xs))
	for _, x := range xs {
		dev = append(dev, math.Abs(x-m))
	}
	scale := median(dev) * 1.4826
	if scale <= 1e-9 {
		scale = 1
	}
	out := make(map[string]float64, len(v))
	for k, x := range v {
		out[k] = (x - m) / scale
	}
	return out
}
