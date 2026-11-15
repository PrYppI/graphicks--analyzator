// Клиент нашего Go API. Все типы повторяют JSON, который отдаёт сервер.

import type { RangeKey } from "./ranges";

export interface CoinSearchItem {
  id: string;
  name: string;
  symbol: string;
  thumb: string;
  rank: number;
}

export interface CoinMarket {
  id: string;
  symbol: string;
  name: string;
  image: string;
  current_price: number;
  price_change_percentage_24h: number;
  market_cap_rank: number;
}

export interface Point {
  t: number; // unix ms
  values: (number | null)[]; // цены по линиям; null — у линии здесь данных нет
}

export interface ChartData {
  ids: string[]; // id монет в порядке линий
  interval: string; // "5m" | "1h" | "1d"
  points: Point[];
}

export interface ChartQuery {
  ids: string[]; // 2–6 монет
  range: RangeKey;
  from?: string; // YYYY-MM-DD, для custom
  to?: string;
  rangeB?: RangeKey; // отдельный период второй линии — при двух одинаковых id
  fromB?: string; // YYYY-MM-DD, для custom
  toB?: string;
}

async function errMessage(res: Response): Promise<string> {
  try {
    const body = await res.json();
    if (body?.error) return body.error;
  } catch {
    // тело не JSON — покажем просто статус
  }
  return `Ошибка HTTP ${res.status}`;
}

export async function searchCoins(q: string, signal?: AbortSignal): Promise<CoinSearchItem[]> {
  const res = await fetch(`/api/search?q=${encodeURIComponent(q)}`, { signal });
  if (!res.ok) throw new Error(await errMessage(res));
  return res.json();
}

export async function fetchMarkets(ids: string[], signal?: AbortSignal): Promise<CoinMarket[]> {
  const res = await fetch(`/api/coins?ids=${encodeURIComponent(ids.join(","))}`, { signal });
  if (!res.ok) throw new Error(await errMessage(res));
  return res.json();
}

export async function fetchChart(q: ChartQuery, signal?: AbortSignal): Promise<ChartData> {
  const params = new URLSearchParams({ ids: q.ids.join(",") });
  if (q.range === "custom") {
    // Кастомный диапазон: сервер сам посчитает дни и обрежет серии.
    if (q.from) params.set("from", q.from);
    if (q.to) params.set("to", q.to);
    if (!q.from) params.set("range", "90"); // пока даты не выбраны — показываем 90 дней
  } else {
    params.set("range", q.range);
  }
  // Отдельный период второй серии — сравнение одной монеты за разные периоды.
  if (q.rangeB) {
    if (q.rangeB === "custom") {
      if (q.fromB) params.set("from2", q.fromB);
      if (q.toB) params.set("to2", q.toB);
      if (!q.fromB) params.set("range2", "90");
    } else {
      params.set("range2", q.rangeB);
    }
  }
  const res = await fetch(`/api/chart?${params.toString()}`, { signal });
  if (!res.ok) throw new Error(await errMessage(res));
  return res.json();
}
