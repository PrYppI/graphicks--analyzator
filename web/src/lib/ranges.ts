// Периоды и режимы отображения — то, чем управляет пользователь.

export const RANGE_PRESETS = [
  { key: "1", label: "24ч" },
  { key: "7", label: "7д" },
  { key: "30", label: "30д" },
  { key: "90", label: "90д" },
  { key: "180", label: "180д" },
  { key: "365", label: "1г" },
  { key: "ytd", label: "YTD" },
] as const;

export type RangeKey = (typeof RANGE_PRESETS)[number]["key"] | "custom";

export function isRangeKey(v: string | null): v is RangeKey {
  return !!v && (RANGE_PRESETS.some(p => p.key === v) || v === "custom");
}

export type ViewMode = "percent" | "index" | "raw" | "ratio";

export const MODES: { key: ViewMode; label: string; hint: string; twoOnly?: boolean }[] = [
  { key: "percent", label: "%", hint: "Изменение в % от начала своего периода" },
  { key: "index", label: "Индекс", hint: "Все линии стартуют со 100" },
  { key: "raw", label: "Цены", hint: "Реальные цены; для сильно разных масштабов включи лог-шкалу" },
  { key: "ratio", label: "A/B", hint: "Отношение цен двух монет: рост линии — первая переигрывает вторую", twoOnly: true },
];

export function isViewMode(v: string | null): v is ViewMode {
  return !!v && MODES.some(m => m.key === v);
}

/** Человекочитаемая подпись периода: «90д», «YTD», «2025», «2025–2026». */
export function periodLabel(range: RangeKey, from: string, to: string): string {
  if (range === "custom" && from) {
    const y1 = from.slice(0, 4);
    const y2 = (to || from).slice(0, 4);
    return y1 === y2 ? y1 : `${y1}–${y2}`;
  }
  if (range === "ytd") return "YTD";
  return RANGE_PRESETS.find(p => p.key === range)?.label ?? range;
}
