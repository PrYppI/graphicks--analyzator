// Селектор монеты: чип с лого и ценой, по клику превращается в поиск.
// Поиск ходит в /api/search с задержкой (debounce 300 мс).

import { useEffect, useRef, useState } from "react";
import { searchCoins } from "../lib/api";
import type { CoinSearchItem, CoinMarket } from "../lib/api";
import { fmtPct, fmtPrice } from "../lib/format";

interface Props {
  color: string;
  label: string; // "A" | "B" — подсказка, когда монета ещё не выбрана
  market?: CoinMarket; // данные выбранной монеты (цена, лого)
  badge?: string; // подпись периода, когда одна монета в обоих слотах
  onPick: (id: string) => void;
}

export function CoinSelect({ color, label, market, badge, onPick }: Props) {
  const [editing, setEditing] = useState(false);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<CoinSearchItem[]>([]);
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);

  // Клик мимо — закрываем редактирование.
  useEffect(() => {
    if (!editing) return;
    const onDoc = (e: MouseEvent) => {
      if (!rootRef.current?.contains(e.target as Node)) {
        setEditing(false);
        setOpen(false);
      }
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [editing]);

  // Поиск с debounce: запрос не чаще, чем раз в 300 мс после остановки ввода.
  // Ту же монету, что уже стоит в другом слоте, НЕ прячем — это легальный
  // выбор: она включает сравнение монеты с самой собой за разные периоды.
  useEffect(() => {
    if (!editing || query.trim().length < 2) {
      setResults([]);
      setOpen(false);
      return;
    }
    const ctrl = new AbortController();
    setLoading(true);
    const timer = setTimeout(() => {
      searchCoins(query, ctrl.signal)
        .then(items => {
          setResults(items);
          setOpen(true);
        })
        .catch(() => {
          if (!ctrl.signal.aborted) {
            setResults([]);
            setOpen(true);
          }
        })
        .finally(() => {
          if (!ctrl.signal.aborted) setLoading(false);
        });
    }, 300);
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  }, [query, editing]);

  const pick = (id: string) => {
    onPick(id);
    setEditing(false);
    setOpen(false);
    setQuery("");
  };

  // Режим чипа: показываем выбранную монету.
  if (!editing) {
    return (
      <button
        className="coin-chip"
        style={{ borderLeftColor: color }}
        onClick={() => setEditing(true)}
        title="Нажмите, чтобы сменить монету"
      >
        {market ? (
          <>
            <img src={market.image} alt="" className="coin-icon" />
            <span className="coin-title">
              <span className="coin-name">{market.name}</span>
              <span className="coin-symbol">
                {market.symbol?.toUpperCase()}
                {badge ? ` · ${badge}` : ""}
              </span>
            </span>
            <span className="coin-price">
              {market.current_price ? (
                <>
                  {fmtPrice(market.current_price)}{" "}
                  <span className={market.price_change_percentage_24h >= 0 ? "coin-up" : "coin-down"}>
                    {fmtPct(market.price_change_percentage_24h)}
                  </span>
                </>
              ) : (
                <span className="muted">нет активных торгов</span>
              )}
            </span>
          </>
        ) : (
          <span className="muted">Монета {label}: нажмите для выбора…</span>
        )}
      </button>
    );
  }

  // Режим поиска.
  return (
    <div ref={rootRef} className="coin-select" style={{ borderLeftColor: color }}>
      <input
        autoFocus
        value={query}
        placeholder="Название или тикер… (Enter — выбрать первую)"
        onChange={e => setQuery(e.target.value)}
        onKeyDown={e => {
          if (e.key === "Enter" && results[0]) pick(results[0].id);
          if (e.key === "Escape") setEditing(false);
        }}
      />
      {loading && <span className="search-spinner" />}
      {open && (
        <ul className="coin-dropdown">
          {results.length === 0 && !loading && (
            <li className="dropdown-note muted">Ничего не найдено. Для DEX-токенов вставь адрес контракта</li>
          )}
          {results.map(item => (
            <li key={item.id} onClick={() => pick(item.id)}>
              {item.thumb ? (
                <img src={item.thumb} alt="" className="coin-icon" />
              ) : (
                <span className="coin-icon coin-icon-empty" />
              )}
              <span className="coin-name">{item.name}</span>
              <span className="coin-symbol">{item.symbol.toUpperCase()}</span>
              {item.rank > 0 && <span className="coin-rank">#{item.rank}</span>}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
