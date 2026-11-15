// Превращение сырых точек графика в серии для выбранного режима сравнения.
// Именно здесь решается главная проблема «BTC против DOGE»: у монет
// несопоставимые цены, поэтому, кроме режима «Цены», всё нормализуется.
// Точки могут содержать null (у линии нет данных в этой точке — период
// другой линии длиннее): такие значения пропускаются, линия обрывается.

import type { Point } from "./api";
import type { ViewMode } from "./ranges";

export interface SeriesDef {
  name: string;
  color: string;
  yAxisIndex: number;
  data: [number, number | null][]; // [unix ms, значение | null]
}

// Конфиги осей ECharts типизируем свободно — воевать с их union-ами не хочется.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type AxisConfig = any;

export interface Transformed {
  series: SeriesDef[];
  yAxes: AxisConfig[];
}

const SPLIT_LINE = { lineStyle: { color: "#1f2937" } };

/**
 * Протягивает последнюю известную цену каждой линии вперёд по общей сетке.
 * У неликвидных токенов свечи есть только в дни торгов — без этого линия
 * рассыпается на штрихи. Между сделками цена и не менялась,
 * так что «ступенька» вместо разрыва — честная картинка. До ПЕРВОЙ точки
 * линии данных нет — там остаётся null (линия начинается, когда токен
 * начал торговаться).
 */
export function fillGaps(points: Point[]): void {
  const last: (number | null)[] = [];
  for (const p of points) {
    p.values.forEach((v, i) => {
      if (v != null) last[i] = v;
      else if (last[i] != null) p.values[i] = last[i];
    });
  }
}

export function transformChart(
  points: Point[],
  mode: ViewMode,
  logScale: boolean,
  names: string[],
  colors: string[],
  sameScale = false, // все линии — одна и та же монета: общая ось цен
): Transformed {
  // База для %/индекса — первая непустая точка КАЖДОЙ линии (при разных
  // периодах линии могут начинаться в разных местах).
  const bases: (number | null)[] = names.map((_, i) => {
    for (const p of points) if (p.values[i] != null) return p.values[i];
    return null;
  });
  const line = (i: number, f: (v: number) => number | null, yAxisIndex = 0): SeriesDef => ({
    name: names[i],
    color: colors[i],
    yAxisIndex,
    data: points.map(p => (p.values[i] == null ? [p.t, null] : [p.t, f(p.values[i]!)])),
  });

  switch (mode) {
    // % от начала своего периода: стандартный способ сравнивать динамику.
    case "percent":
      return {
        series: names.map((_, i) =>
          bases[i] == null ? line(i, () => null) : line(i, v => (v / bases[i]! - 1) * 100),
        ),
        yAxes: [{ type: "value", scale: true, splitLine: SPLIT_LINE, axisLabel: { formatter: "{value}%", color: "#9ca3af" } }],
      };

    // Индекс = 100 — то же сравнение, но в «презентационном» виде.
    case "index":
      return {
        series: names.map((_, i) =>
          bases[i] == null ? line(i, () => null) : line(i, v => (v / bases[i]!) * 100),
        ),
        yAxes: [{ type: "value", scale: true, splitLine: SPLIT_LINE, axisLabel: { color: "#9ca3af" } }],
      };

    // Реальные цены, лог-шкала по желанию. Две разные монеты — своя ось
    // на каждую (левая A, правая B). Одна монета за разные периоды или
    // 3+ линии — общая ось (для сильно разных масштабов включай лог).
    case "raw": {
      const shared = sameScale || names.length > 2;
      const series = names.map((_, i) => line(i, v => v, shared ? 0 : i));
      const axes: AxisConfig[] = shared
        ? [priceAxis(logScale)]
        : [priceAxis(logScale), { ...priceAxis(logScale), splitLine: { show: false } }];
      // На лог-шкале ECharts подписывает только степени десятки (1 и 10) —
      // для коротких диапазонов добавляем деления 2…9 сами.
      if (logScale) {
        let mn = Infinity;
        let mx = -Infinity;
        for (const p of points) {
          for (const v of p.values) {
            if (v != null) {
              mn = Math.min(mn, v);
              mx = Math.max(mx, v);
            }
          }
        }
        if (Number.isFinite(mn) && mn > 0) {
          for (const ax of axes) {
            ax.min = mn / 1.5;
            ax.max = mx * 1.5;
            // Если данные выше единицы, подписи ниже 1 не нужны.
            ax.axisLabel.customValues = logTicks(mn >= 1 ? Math.max(ax.min, 1) : ax.min, ax.max);
          }
        }
      }
      return { series, yAxes: axes };
    }

    // Отношение цен A/B: линия растёт — первая монета переигрывает вторую.
    // Доступно только при двух линиях (кнопка задизейблена иначе).
    case "ratio":
      return {
        series: [
          {
            name: names[0],
            color: colors[0],
            yAxisIndex: 0,
            data: points.map(p =>
              p.values[0] == null || p.values[1] == null
                ? [p.t, null]
                : [p.t, p.values[0]! / p.values[1]!],
            ),
          },
        ],
        yAxes: [{ type: "value", scale: true, splitLine: SPLIT_LINE, axisLabel: { color: "#9ca3af" } }],
      };
  }
}

function priceAxis(logScale: boolean): AxisConfig {
  return {
    type: logScale ? "log" : "value",
    scale: !logScale,
    axisLabel: { color: "#9ca3af", formatter: fmtAxisPrice },
    splitLine: SPLIT_LINE,
  };
}

// Деления лог-шкалы: 1, 2, … 9, 10, 20, … в диапазоне данных.
// Если делений набирается много, оставляем разреженный шаг 1-2-5.
function logTicks(min: number, max: number): number[] {
  const out: number[] = [];
  const start = Math.floor(Math.log10(min));
  const end = Math.ceil(Math.log10(max));
  for (let d = start; d <= end; d++) {
    for (let m = 1; m <= 9; m++) {
      const v = m * Math.pow(10, d);
      if (v >= min && v <= max) {
        out.push(Number(v.toPrecision(12)));
      }
    }
  }
  if (out.length > 12) {
    return out.filter(v => {
      const m = Number(String(v.toExponential()).split("e")[0]);
      return [1, 2, 5].includes(Math.round(m * 10) / 10);
    });
  }
  return out;
}

// Компактные подписи цен на оси: 120k, 2.2k, 3.5, 0.0021.
function fmtAxisPrice(v: number): string {
  if (v >= 1e6) return (v / 1e6).toFixed(1) + "M";
  if (v >= 10000) return (v / 1000).toFixed(0) + "k";
  if (v >= 1000) return (v / 1000).toFixed(1) + "k";
  if (v >= 1) return String(Math.round(v * 100) / 100);
  return v.toPrecision(2);
}
