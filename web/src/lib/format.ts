// Числа и даты в человеческом виде.

const priceInt = new Intl.NumberFormat("ru-RU", { maximumFractionDigits: 0 });
const priceDec = new Intl.NumberFormat("ru-RU", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const priceTiny = new Intl.NumberFormat("ru-RU", { maximumSignificantDigits: 4 });

/** Цена с $: 97 420 $ для биткоина, 0,1234 $ для «мелочи». */
export function fmtPrice(v: number): string {
  if (!Number.isFinite(v)) return "—";
  if (Math.abs(v) >= 1000) return "$" + priceInt.format(v);
  if (Math.abs(v) >= 1) return "$" + priceDec.format(v);
  return "$" + priceTiny.format(v);
}

/** Обычное число (для ratio) с разумной точностью. */
export function fmtNum(v: number): string {
  if (!Number.isFinite(v)) return "—";
  if (Math.abs(v) >= 1000) return v.toFixed(0);
  if (Math.abs(v) >= 1) return v.toFixed(2);
  return v.toPrecision(3);
}

/** Проценты со знаком: +12,34% / −3,05%. */
export function fmtPct(v: number): string {
  if (!Number.isFinite(v)) return "—";
  return (v > 0 ? "+" : "") + v.toFixed(2) + "%";
}

export function fmtDateTime(ms: number): string {
  return new Date(ms).toLocaleString("ru-RU", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Дата в формате YYYY-MM-DD по локальному времени (для <input type="date">). */
export function isoDate(d: Date): string {
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}
