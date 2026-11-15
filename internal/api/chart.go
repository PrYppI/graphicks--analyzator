// Chart и всё, что с ним связано: разбор периода, загрузка серий из
// источников, выравнивание N серий на общую сетку времени.
package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- Типы ответа /api/chart ---

// Point — одна точка графика: время и цены всех линий. nil значит «у этой
// линии в этой точке данных нет»: истории и периоды разной длины, каждая
// линия рисуется весь свой период, остальные в этой точке отсутствуют.
type Point struct {
	T      int64      `json:"t"` // unix ms
	Values []*float64 `json:"values"`
}

func fptr(v float64) *float64 { return &v }

// ChartData — ответ GET /api/chart.
type ChartData struct {
	IDs      []string `json:"ids"` // id монет в порядке линий
	Interval string   `json:"interval"`
	Points   []Point  `json:"points"`
}

// --- GET /api/chart?ids=a,b,c&range=90 | &from=2025-01-01&to=2025-06-01 ---
//
// Сравнение одной монеты с самой собой за другой период:
// ids=bitcoin,bitcoin + отдельный период второй линии — range2=...
// (или from2/to2). Тогда линии выравниваются не по абсолютному времени,
// а «от начала периода».

// maxChartCoins — верхняя граница слотов: цвета линий на фронтенде
// заведены под 5, сервер допускает с запасом.
const maxChartCoins = 6

// Chart — главный хендлер: истории цен от 2 до 6 монет, выровненные
// по времени. Сравнение одной монеты за разные периоды: ids с двумя
// одинаковыми id + отдельный период второй линии (range2/from2).
func (s *Server) Chart(w http.ResponseWriter, r *http.Request) {
	ids := strings.Split(r.URL.Query().Get("ids"), ",")
	if n := len(ids); n < 2 || n > maxChartCoins {
		writeErr(w, http.StatusBadRequest, "нужно от 2 до 6 монет: ?ids=bitcoin,ethereum,solana")
		return
	}
	const maxIDLen = 140 // реальные id короче; длинное — попытка мусора в URL источников
	for _, id := range ids {
		if id == "" || len(id) > maxIDLen {
			writeErr(w, http.StatusBadRequest, "в ids есть пустой или слишком длинный id")
			return
		}
	}

	pa, err := parsePeriod(r.URL.Query(), "")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Периоды всех линий: по умолчанию — период A. Отдельный период
	// (range2/from2) может иметь только ВТОРАЯ линия — это режим
	// «одна монета за разные периоды», когда первые два id совпадают.
	periods := make([]period, len(ids))
	for i := range periods {
		periods[i] = pa
	}
	if r.URL.Query().Has("range2") || r.URL.Query().Has("from2") {
		pb, err := parsePeriod(r.URL.Query(), "2")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		periods[1] = pb
	}

	seriesList, err := s.seriesAll(r.Context(), ids, "usd", periods)
	if err != nil {
		httpError(w, s.log, err)
		return
	}

	// Каждую серию режем по СВОЕМУ окну до всякого выравнивания.
	for i := range seriesList {
		seriesList[i] = trimSeries(seriesList[i], periods[i].fromMs, periods[i].toMs)
		if len(seriesList[i]) == 0 {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("нет данных по %s за выбранный период — возможно, монета торговалась позже", ids[i]))
			return
		}
	}

	// Один период у всех линий — сетка абсолютного времени; разные —
	// совмещение «от начала периода».
	relative := false
	for i := 1; i < len(periods); i++ {
		if periods[i] != periods[0] {
			relative = true
		}
	}

	points, interval, err := alignAll(seriesList, relative)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ChartData{
		IDs:      ids,
		Interval: interval,
		Points:   points,
	})
}

// --- Период запроса ---

// period — разобранный период запроса: что просить у источника
// и как обрезать результат до нужного окна.
type period struct {
	days         string // "1".."365" или "max"
	fromMs, toMs int64  // границы обрезки (0 = без ограничения)
}

// parsePeriod разбирает параметры периода с суффиксом: "" для первой
// линии, "2" — для второй при сравнении одной монеты за разные периоды.
// Кастомный диапазон: from=2025-01-01&to=2025-06-01 (to можно не указывать).
func parsePeriod(q url.Values, suffix string) (period, error) {
	rangeKey, fromKey, toKey := "range"+suffix, "from"+suffix, "to"+suffix

	if f := q.Get(fromKey); f != "" {
		from, err := time.Parse("2006-01-02", f)
		if err != nil {
			return period{}, errors.New(fromKey + ": ожидается дата в формате ГГГГ-ММ-ДД")
		}
		to := time.Now()
		if t := q.Get(toKey); t != "" {
			to, err = time.Parse("2006-01-02", t)
			if err != nil {
				return period{}, errors.New(toKey + ": ожидается дата в формате ГГГГ-ММ-ДД")
			}
			to = to.AddDate(0, 0, 1) // включаем выбранный день целиком
		}
		if to.After(time.Now()) {
			to = time.Now()
		}
		if !to.After(from) {
			return period{}, errors.New(toKey + " должен быть позже " + fromKey)
		}
		p := period{fromMs: from.UnixMilli(), toMs: to.UnixMilli()}
		// CoinGecko всегда отдаёт окно «N дней назад от сейчас»,
		// поэтому days считаем от from до сегодня, а до нужного окна
		// результат потом обрежется (trimSeries). Бесплатный тариф отдаёт
		// максимум год данных; глубже — просим всю историю (дневную).
		if total := int(time.Since(from).Hours()/24) + 1; total <= 365 {
			p.days = strconv.Itoa(total)
		} else {
			p.days = "max"
		}
		return p, nil
	}

	switch r := q.Get(rangeKey); r {
	case "ytd":
		// С начала текущего года.
		now := time.Now()
		jan1 := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)
		d := int(now.Sub(jan1).Hours()/24) + 1
		return period{days: strconv.Itoa(d), fromMs: jan1.UnixMilli()}, nil
	case "1", "7", "14", "30", "90", "180", "365":
		// Нижняя граница нужна и пресетам: DEX-токены отдают всю историю
		// пула разом, и без обрезки «90д» рисовался бы целый год.
		n, _ := strconv.Atoi(r)
		from := time.Now().AddDate(0, 0, -n)
		return period{days: r, fromMs: from.UnixMilli()}, nil
	case "max":
		return period{days: "max"}, nil
	case "":
		from := time.Now().AddDate(0, 0, -90)
		return period{days: "90", fromMs: from.UnixMilli()}, nil // период по умолчанию
	default:
		return period{}, errors.New(rangeKey + ": допустимо 1, 7, 14, 30, 90, 180, 365, max, ytd или " + fromKey + "/" + toKey)
	}
}

// --- Выравнивание серий ---

// alignAll приводит N серий цен к общей сетке времени.
// Шаг сетки каждой серии — медиана разниц соседних точек; таймстемпы
// округляются вниз до шага (в «корзине» остаётся последняя цена).
//
// relative=false: серии сняты за один период — сетка абсолютного времени.
// relative=true: серии сняты за РАЗНЫЕ периоды — каждая совмещается
// «от начала своего периода» (смещение от первой точки серии), а ось
// времени получает даты первой серии, поверх которых ложатся остальные.
//
// Объединение корзин, а не пересечение: у каждой линии весь её период,
// чего нет у линии — nil, в этом месте она заканчивается.
func alignAll(seriesList [][][]float64, relative bool) ([]Point, string, error) {
	maps := make([]map[int64]float64, 0, len(seriesList))
	steps := make([]int64, 0, len(seriesList))
	var base int64
	for i, s := range seriesList {
		if len(s) == 0 {
			return nil, "", fmt.Errorf("серия %d пуста", i)
		}
		step := gridStep(s)
		if relative {
			maps = append(maps, offsetMap(s, step))
		} else {
			maps = append(maps, bucketize(s, step))
		}
		steps = append(steps, step)
		if i == 0 {
			base = int64(s[0][0])
		}
	}
	if len(maps) < 2 {
		return nil, "", errors.New("источники вернули пустую историю цен")
	}

	// Объединение ключей всех серий по возрастанию времени.
	keySet := make(map[int64]bool)
	for _, m := range maps {
		for k := range m {
			keySet[k] = true
		}
	}
	keys := make([]int64, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	if len(keys) < 2 {
		return nil, "", errors.New("серий не удалось сопоставить по времени")
	}

	points := make([]Point, 0, len(keys))
	for _, k := range keys {
		t := k
		if relative {
			t = base + k
		}
		p := Point{T: t, Values: make([]*float64, len(maps))}
		for i, m := range maps {
			if v, ok := m[k]; ok {
				p.Values[i] = fptr(v)
			}
		}
		points = append(points, p)
	}
	return points, intervalLabel(steps[0]), nil
}

// gridStep оценивает шаг сетки как медиану интервалов между соседними точками.
func gridStep(prices [][]float64) int64 {
	if len(prices) < 2 {
		return 0
	}
	diffs := make([]int64, 0, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		d := int64(prices[i][0] - prices[i-1][0])
		if d > 0 {
			diffs = append(diffs, d)
		}
	}
	if len(diffs) == 0 {
		return 0
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return diffs[len(diffs)/2]
}

// bucketize группирует точки по корзинам длиной bucketMs;
// при нескольких точках в корзине остаётся последняя.
func bucketize(prices [][]float64, bucketMs int64) map[int64]float64 {
	m := make(map[int64]float64, len(prices))
	for _, p := range prices {
		ts, price := int64(p[0]), p[1]
		if bucketMs > 0 {
			ts = ts / bucketMs * bucketMs // округление вниз до шага сетки
		}
		m[ts] = price
	}
	return m
}

// offsetMap индексирует серию по смещению от её первой точки,
// округлённому вниз до шага сетки; в корзине остаётся последняя цена.
func offsetMap(prices [][]float64, step int64) map[int64]float64 {
	base := int64(prices[0][0])
	m := make(map[int64]float64, len(prices))
	for _, p := range prices {
		off := int64(p[0]) - base
		if step > 0 {
			off = off / step * step
		}
		m[off] = p[1]
	}
	return m
}

// intervalLabel — человекочитаемая гранулярность для фронтенда.
func intervalLabel(bucketMs int64) string {
	switch {
	case bucketMs <= 6*60*1000:
		return "5m"
	case bucketMs <= 2*60*60*1000:
		return "1h"
	default:
		return "1d"
	}
}

// trimSeries оставляет точки серии в границах [fromMs, toMs];
// нулевая граница = без ограничения.
func trimSeries(prices [][]float64, fromMs, toMs int64) [][]float64 {
	if fromMs == 0 && toMs == 0 {
		return prices
	}
	out := prices[:0:0] // новая ёмкость: не трогаем массив из кэша
	for _, p := range prices {
		if (fromMs == 0 || int64(p[0]) >= fromMs) && (toMs == 0 || int64(p[0]) <= toMs) {
			out = append(out, p)
		}
	}
	return out
}
