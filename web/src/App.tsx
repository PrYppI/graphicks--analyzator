// Главный экран: слоты монет (2–5), панель управления и график.
// Всё состояние синхронизируется с URL — сравнение можно отправить ссылкой.
//
// Особые режимы:
//  • одна и та же монета в первых двух слотах — сравнение её за разные
//    периоды (у второй линии свой период, линии совмещаются «от начала
//    периода» — это делает сервер);
//  • 3+ линии в режиме «Цены» — общая ось (для сильно разных масштабов
//    включай лог-шкалу).

import { Fragment, useEffect, useState } from "react";
import { CoinSelect } from "./components/CoinSelect";
import { PriceChart } from "./components/PriceChart";
import { fetchChart, fetchMarkets } from "./lib/api";
import type { ChartData, CoinMarket } from "./lib/api";
import { MODES, RANGE_PRESETS, isRangeKey, isViewMode, periodLabel } from "./lib/ranges";
import type { RangeKey, ViewMode } from "./lib/ranges";
import { isoDate } from "./lib/format";
import { fillGaps } from "./lib/transform";

const MAX_SLOTS = 5;
const MIN_SLOTS = 2;

// Цвета слотов — зашиты в линиях графика, чипах и тултипе.
const COLORS = ["#38bdf8", "#fb923c", "#34d399", "#a78bfa", "#f472b6"];

interface AppState {
  coins: string[]; // id монет по слотам; пустая строка = слот ещё не выбран
  range: RangeKey;
  from: string; // YYYY-MM-DD — для custom
  to: string;
  rangeB: RangeKey; // период второй линии — активен при coins[0] === coins[1]
  fromB: string;
  toB: string;
  mode: ViewMode;
  log: boolean;
}

const MODE_HINTS: Record<ViewMode, string> = {
  percent: "Линии показывают изменение в % от начала своего периода",
  index: "Все линии стартуют со 100",
  raw: "Две разные монеты — свои оси; одна монета или 3+ — общая ось (лог-шкала спасает масштабы)",
  ratio: "Линия = цена A / цена B: растёт — монета A переигрывает",
};

const INTERVAL_LABELS: Record<string, string> = {
  "5m": "5-минутные данные",
  "1h": "часовые данные",
  "1d": "дневные данные",
};

// Начальное состояние читаем из URL:
// /compare/bitcoin/ethereum?range=90&mode=percent
// /compare/bitcoin/bitcoin?range2=custom&from2=2025-01-01&to2=2025-12-31
// /compare/gt%3Aton%2FEQ.../gt%3Aton%2FEQ... — DEX-токены по адресу
function stateFromURL(): AppState {
  const u = new URL(window.location.href);
  const parts = u.pathname.match(/^\/compare\/(.+)$/);
  const sp = u.searchParams;
  const range = sp.get("range");
  const range2 = sp.get("range2");
  const mode = sp.get("mode");
  const from = sp.get("from") ?? "";
  const to = sp.get("to") ?? "";
  const resolvedRange: RangeKey = isRangeKey(range) ? range : from ? "custom" : "90";

  // Слоты: раскодируем сегменты пути; меньше двух — дефолтная пара.
  let coins: string[] = [];
  if (parts) {
    coins = parts[1]
      .split("/")
      .map(s => {
        try {
          return decodeURIComponent(s);
        } catch {
          return s;
        }
      })
      .filter(s => s !== "");
  }
  if (coins.length < 2) {
    coins = ["bitcoin", "ethereum"];
  }

  return {
    coins: coins.slice(0, MAX_SLOTS),
    range: resolvedRange,
    from,
    to,
    // Период B из ссылки; без него — зеркалим период A.
    rangeB: isRangeKey(range2) ? range2 : resolvedRange,
    fromB: sp.get("from2") ?? "",
    toB: sp.get("to2") ?? "",
    mode: isViewMode(mode) ? mode : "percent",
    log: sp.get("log") === "1",
  };
}

export default function App() {
  const [state, setState] = useState<AppState>(stateFromURL);
  const [chart, setChart] = useState<ChartData | null>(null);
  const [chartLoading, setChartLoading] = useState(true);
  const [chartError, setChartError] = useState<string | null>(null);
  const [markets, setMarkets] = useState<Record<string, CoinMarket>>({});
  const [reloadNonce, setReloadNonce] = useState(0); // кнопка «повторить» при ошибке

  const patch = (p: Partial<AppState>) => setState(s => ({ ...s, ...p }));

  const coins = state.coins;
  const ready = coins.every(c => c !== ""); // все слоты заполнены
  const coinsKey = coins.join("|");

  // Особый режим: первые два слота — одна монета → «Период второй линии».
  const pairSame = coins[0] === coins[1];
  // Все слоты — одна монета → в «Ценах» общая ось.
  const allSame = coins.every(c => c === coins[0]);

  // Синхронизация состояния с адресной строкой (replace, чтобы не мусорить в истории).
  useEffect(() => {
    const q = new URLSearchParams();
    if (state.range !== "90") q.set("range", state.range);
    if (state.range === "custom" && state.from) q.set("from", state.from);
    if (state.range === "custom" && state.to) q.set("to", state.to);
    // Период B пишем в ссылку, только если он отличается от периода A
    // (или это custom — там важны from2/to2).
    if (pairSame && (state.rangeB !== state.range || state.rangeB === "custom")) {
      q.set("range2", state.rangeB);
    }
    if (pairSame && state.rangeB === "custom" && state.fromB) q.set("from2", state.fromB);
    if (pairSame && state.rangeB === "custom" && state.toB) q.set("to2", state.toB);
    if (state.mode !== "percent") q.set("mode", state.mode);
    if (state.log) q.set("log", "1");
    const qs = q.toString();
    const path = `/compare/${coins.filter(c => c).map(encodeURIComponent).join("/")}`;
    window.history.replaceState(null, "", `${path}${qs ? "?" + qs : ""}`);

    document.title = names()
      .map(n => n.split(" ")[0])
      .join(" vs ") + " — сравнение монет";
  }, [state, markets, pairSame, coins]);

  // Подписи линий: при сравнении с собой — с периодом.
  function names(): string[] {
    const periodA = periodLabel(state.range, state.from, state.to);
    const periodB = periodLabel(state.rangeB, state.fromB, state.toB);
    return coins.map((id, i) => {
      const sym = markets[id]?.symbol?.toUpperCase() ?? id.toUpperCase();
      if (!pairSame) return sym;
      return i === 0 ? `${sym} ${periodA}` : i === 1 ? `${sym} ${periodB}` : sym;
    });
  }

  // История цен для графика.
  useEffect(() => {
    if (!ready) {
      setChartLoading(false);
      return;
    }
    const ctrl = new AbortController();
    setChartLoading(true);
    setChartError(null);
    fetchChart(
      {
        ids: coins,
        range: state.range,
        from: state.from,
        to: state.to,
        rangeB: pairSame ? state.rangeB : undefined,
        fromB: pairSame ? state.fromB : undefined,
        toB: pairSame ? state.toB : undefined,
      },
      ctrl.signal,
    )
      .then(data => {
        // Заполняем пробелы неликвидных токенов: иначе ECharts рвёт
        // линию на каждом дне без сделок.
        fillGaps(data.points);
        setChart(data);
        setChartError(null);
      })
      .catch((e: unknown) => {
        if (!ctrl.signal.aborted) {
          setChartError(e instanceof Error ? e.message : String(e));
        }
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setChartLoading(false);
      });
  }, [coinsKey, ready, pairSame, state.range, state.from, state.to, state.rangeB, state.fromB, state.toB, reloadNonce]);

  // «Живые» данные выбранных монет для чипов (цена, 24ч).
  useEffect(() => {
    const ids = coins.filter(c => c);
    if (ids.length === 0) return;
    const ctrl = new AbortController();
    fetchMarkets(ids, ctrl.signal)
      .then(list => {
        const m: Record<string, CoinMarket> = {};
        for (const item of list) m[item.id] = item;
        setMarkets(m);
      })
      .catch(() => {}); // не критично: чипы просто останутся без цены
    return () => ctrl.abort();
  }, [coinsKey, reloadNonce]);

  // Выбор монеты в слоте i. Если в первые два слота попала одна и та же
  // монета — включается сравнение её за разные периоды.
  const pick = (i: number, id: string) =>
    setState(s => ({ ...s, coins: s.coins.map((c, j) => (j === i ? id : c)) }));
  const addSlot = () =>
    setState(s =>
      s.coins.length >= MAX_SLOTS ? s : { ...s, coins: [...s.coins, ""] },
    );
  const removeSlot = (i: number) =>
    setState(s =>
      s.coins.length <= MIN_SLOTS ? s : { ...s, coins: s.coins.filter((_, j) => j !== i) },
    );

  // Клик по «свой» для основного периода: разумные даты по умолчанию.
  const startCustom = () => {
    patch({
      range: "custom",
      from: state.from || isoDate(new Date(Date.now() - 90 * 86400000)),
      to: state.to || isoDate(new Date()),
    });
  };

  // Клик по «свой» для периода второй линии: по умолчанию — весь прошлый
  // год, это самый частый сценарий («сравнить с прошлым годом»).
  const startCustomB = () => {
    const y = new Date().getFullYear() - 1;
    patch({
      rangeB: "custom",
      fromB: state.fromB || `${y}-01-01`,
      toB: state.toB || `${y}-12-31`,
    });
  };

  const periodA = periodLabel(state.range, state.from, state.to);
  const periodB = periodLabel(state.rangeB, state.fromB, state.toB);
  const lineNames = names();

  return (
    <div className="app">
      <header className="topbar">
        <h1>Сравнение монет</h1>
        <span className="muted">
          {pairSame ? "одна монета за разные периоды" : "график одной монеты поверх другой"}
        </span>
      </header>

      <div className="selectors">
        {coins.map((id, i) => (
          <Fragment key={i}>
            {i > 0 && <span className="vs">vs</span>}
            <div className="slot">
              <CoinSelect
                color={COLORS[i]}
                label={String(i + 1)}
                market={id ? markets[id] : undefined}
                badge={pairSame && i < 2 ? (i === 0 ? periodA : periodB) : undefined}
                onPick={picked => pick(i, picked)}
              />
              {coins.length > MIN_SLOTS && (
                <button
                  className="slot-remove"
                  title="Убрать монету"
                  onClick={() => removeSlot(i)}
                >
                  ×
                </button>
              )}
            </div>
          </Fragment>
        ))}
        {coins.length < MAX_SLOTS && (
          <button className="add-slot" title="Добавить ещё одну монету" onClick={addSlot}>
            +
          </button>
        )}
      </div>

      <div className="controls">
        <div className="tabs">
          {RANGE_PRESETS.map(p => (
            <button
              key={p.key}
              className={"tab" + (state.range === p.key ? " active" : "")}
              onClick={() => patch({ range: p.key })}
            >
              {p.label}
            </button>
          ))}
          <button className={"tab" + (state.range === "custom" ? " active" : "")} onClick={startCustom}>
            свой
          </button>
        </div>
        <div className="tabs">
          {MODES.map(m => {
            const off = m.twoOnly === true && coins.length !== 2;
            return (
              <button
                key={m.key}
                className={"tab" + (state.mode === m.key ? " active" : "")}
                title={off ? "Доступно только для двух монет" : m.hint}
                disabled={off}
                onClick={() => patch({ mode: m.key })}
              >
                {m.label}
              </button>
            );
          })}
          {state.mode === "raw" && (
            <label className="log-toggle">
              <input type="checkbox" checked={state.log} onChange={e => patch({ log: e.target.checked })} />
              лог-шкала
            </label>
          )}
        </div>
      </div>

      {state.range === "custom" && (
        <CustomDates from={state.from} to={state.to} onFrom={v => patch({ from: v })} onTo={v => patch({ to: v })} />
      )}

      {/* Вторая линия той же монеты — свой период */}
      {pairSame && (
        <div className="period-row">
          <span className="period-label">Период второй линии:</span>
          <div className="tabs">
            {RANGE_PRESETS.map(p => (
              <button
                key={p.key}
                className={"tab" + (state.rangeB === p.key ? " active" : "")}
                onClick={() => patch({ rangeB: p.key })}
              >
                {p.label}
              </button>
            ))}
            <button className={"tab" + (state.rangeB === "custom" ? " active" : "")} onClick={startCustomB}>
              свой
            </button>
          </div>
        </div>
      )}
      {pairSame && state.rangeB === "custom" && (
        <CustomDates from={state.fromB} to={state.toB} onFrom={v => patch({ fromB: v })} onTo={v => patch({ toB: v })} />
      )}

      {chartError && (
        <div className="error-banner">
          {chartError}{" "}
          <button className="retry" onClick={() => setReloadNonce(n => n + 1)}>
            повторить
          </button>
        </div>
      )}

      <div className={"chart-wrap" + (chartLoading ? " loading" : "")}>
        {ready && chart && (
          <PriceChart
            chart={chart}
            names={lineNames}
            colors={COLORS}
            mode={state.mode}
            logScale={state.log}
            sameScale={allSame}
          />
        )}
        {chartLoading && <div className="chart-spinner">Загружаем цены…</div>}
      </div>

      <footer className="hint">
        {MODE_HINTS[state.mode]}
        {pairSame ? " · линии совмещены по началу периода" : ""}
        {chart && INTERVAL_LABELS[chart.interval] ? ` · ${INTERVAL_LABELS[chart.interval]}` : ""}
        {" · "}колесо — зум, перетаскивание — панорама
      </footer>
    </div>
  );
}

// Поля дат + быстрые кнопки «весь год»: один клик — и период равен
// календарному году (2025-01-01 … 2025-12-31).
function CustomDates({
  from,
  to,
  onFrom,
  onTo,
}: {
  from: string;
  to: string;
  onFrom: (v: string) => void;
  onTo: (v: string) => void;
}) {
  const thisYear = new Date().getFullYear();
  const years = [thisYear, thisYear - 1, thisYear - 2, thisYear - 3];
  const today = isoDate(new Date());
  const pickYear = (y: number) => {
    onFrom(`${y}-01-01`);
    onTo(y === thisYear ? today : `${y}-12-31`);
  };
  return (
    <div className="custom-range">
      <input type="date" value={from} max={today} onChange={e => onFrom(e.target.value)} />
      <span className="muted">—</span>
      <input type="date" value={to} max={today} min={from || undefined} onChange={e => onTo(e.target.value)} />
      <div className="year-chips">
        {years.map(y => (
          <button key={y} className="tab" onClick={() => pickYear(y)}>
            {y}
          </button>
        ))}
      </div>
    </div>
  );
}
