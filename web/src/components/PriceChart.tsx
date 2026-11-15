// Обёртка над ECharts: инициализация канваса, ресайз и сборка опций.
// Логика «что рисовать» лежит в lib/transform.ts, здесь — только как.

import { useEffect, useRef } from "react";
import * as echarts from "echarts";
import type { ChartData } from "../lib/api";
import type { ViewMode } from "../lib/ranges";
import { transformChart } from "../lib/transform";
import { fmtDateTime, fmtNum, fmtPct, fmtPrice } from "../lib/format";

interface Props {
  chart: ChartData;
  names: string[]; // подписи линий (с периодами, если сравнение с собой)
  colors: string[];
  mode: ViewMode;
  logScale: boolean;
  sameScale: boolean; // все линии — одна и та же монета: общая ось цен
}

export function PriceChart({ chart, names, colors, mode, logScale, sameScale }: Props) {
  const elRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<echarts.ECharts | null>(null);
  const namesKey = names.join("|");

  // Инициализация один раз на смонтированный div.
  useEffect(() => {
    const el = elRef.current;
    if (!el) return;
    const c = echarts.init(el);
    chartRef.current = c;
    const ro = new ResizeObserver(() => c.resize());
    ro.observe(el);
    return () => {
      ro.disconnect();
      c.dispose();
      chartRef.current = null;
    };
  }, []);

  // Перестраиваем график при изменении данных или настроек.
  // Если сетка точек не изменилась (сменился только режим/шкала) —
  // сохраняем текущее положение зума, чтобы сравнение не «прыгало».
  useEffect(() => {
    const c = chartRef.current;
    if (!c) return;
    const n = chart.points.length;
    const grid = `${n}:${chart.points[0]?.t}:${chart.points[n - 1]?.t}`;
    const sameGrid = grid === (c as any).__gridKey;
    (c as any).__gridKey = grid;
    c.setOption(buildOption(chart, names, colors, mode, logScale, sameScale, sameGrid ? c : null), true);
  }, [chart, namesKey, colors, mode, logScale, sameScale]);

  return <div ref={elRef} className="chart" />;
}

function buildOption(
  chart: ChartData,
  names: string[],
  colors: string[],
  mode: ViewMode,
  logScale: boolean,
  sameScale: boolean,
  preserveZoomFrom: echarts.ECharts | null,
): echarts.EChartsOption {
  const t = transformChart(chart.points, mode, logScale, names, colors, sameScale);

  const opt: any = {
    animation: false,
    backgroundColor: "transparent",
    grid: { left: 12, right: 12, top: 28, bottom: 72, containLabel: true },
    tooltip: {
      trigger: "axis",
      axisPointer: { type: "cross", label: { backgroundColor: "#1f2937", color: "#e5e7eb" } },
      backgroundColor: "#111827",
      borderColor: "#374151",
      textStyle: { color: "#e5e7eb", fontSize: 12 },
      formatter: (params: any) => tooltipHTML(params, chart, names, colors, mode),
    },
    xAxis: {
      type: "time",
      axisLine: { lineStyle: { color: "#374151" } },
      axisLabel: { color: "#9ca3af", hideOverlap: true },
      splitLine: { show: false },
    },
    yAxis: t.yAxes,
    dataZoom: [
      { type: "inside" },
      {
        type: "slider",
        height: 22,
        bottom: 10,
        borderColor: "#374151",
        backgroundColor: "#0b0f14",
        fillerColor: "rgba(56,189,248,0.10)",
        handleStyle: { color: "#334155", borderColor: "#4b5563" },
        moveHandleStyle: { color: "#334155" },
        textStyle: { color: "#9ca3af", fontSize: 10 },
        dataBackground: {
          lineStyle: { color: "#374151" },
          areaStyle: { color: "#111827" },
        },
      },
    ],
    series: t.series.map(s => ({
      name: s.name,
      type: "line",
      data: s.data,
      yAxisIndex: s.yAxisIndex,
      showSymbol: false,
      sampling: "lttb", // не убиться на тысячах точек при зуме
      lineStyle: { width: 2, color: s.color },
      itemStyle: { color: s.color },
      emphasis: { focus: "series" },
    })),
  };

  // Переносим положение зума из старого графика.
  if (preserveZoomFrom) {
    const old = (preserveZoomFrom.getOption() as any)?.dataZoom?.[0];
    if (old && old.start != null) {
      for (const dz of opt.dataZoom) {
        dz.start = old.start;
        dz.end = old.end;
      }
    }
  }
  return opt as echarts.EChartsOption;
}

// Тултип: цены всех линий, изменение от начала их периодов; ratio —
// только при двух линиях. Нет данных у линии в этой точке — прочерк.
//
// ВАЖНО: имена линий приходят из внешних API (тикеры шиткоинов — кто
// угодно) и тултип рендерится как HTML. Всё внешнее — через esc(),
// иначе вредоносный тикер вида "<img onerror=…>" исполнится в браузере.
function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function tooltipHTML(
  params: any[],
  chart: ChartData,
  names: string[],
  colors: string[],
  mode: ViewMode,
): string {
  const t = Number(params[0]?.axisValue);
  const p = chart.points.find(x => x.t === t);
  if (!p) return "";

  const norm = mode === "percent" || mode === "index"; // главным числом показываем %

  // База каждой линии — её первая непустая точка.
  const bases = chart.ids.map((_, i) => {
    for (const q of chart.points) if (q.values[i] != null) return q.values[i]!;
    return null;
  });

  const row = (i: number) => {
    const v = p.values[i];
    const base = bases[i];
    const pct = v == null || base == null ? null : (v / base - 1) * 100;
    return (
      `<div class="tt-row">` +
      `<span class="tt-dot" style="background:${colors[i]}"></span>` +
      `<span class="tt-sym">${esc(names[i])}</span>` +
      (norm
        ? `<span class="tt-main">${pct == null ? "—" : fmtPct(pct)}</span><span class="tt-muted">${v == null ? "" : fmtPrice(v)}</span>`
        : `<span class="tt-main">${v == null ? "—" : fmtPrice(v)}</span><span class="tt-muted">${pct == null ? "" : fmtPct(pct)}</span>`) +
      `</div>`
    );
  };

  let html = `<div class="tt-title">${fmtDateTime(t)}</div>`;
  html += names.map((_, i) => row(i)).join("");
  if (names.length === 2 && p.values[0] != null && p.values[1] != null) {
    html +=
      `<div class="tt-row">` +
      `<span class="tt-dot" style="background:#6b7280"></span>` +
      `<span class="tt-sym">${esc(names[0] + "/" + names[1])}</span>` +
      `<span class="tt-main">${fmtNum(p.values[0]! / p.values[1]!)}</span>` +
      `</div>`;
  }
  return html;
}
