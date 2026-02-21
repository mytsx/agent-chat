import { useEffect, useState } from "react";
import { CUSTOM_LAYOUT, isCustomLayout, parseGrid } from "../lib/types";

const MIN_COLS = 1;
const MAX_COLS = 8;
const MIN_ROWS = 1;
const MAX_ROWS = 8;

function clamp(value: number, min: number, max: number): number {
  return Math.max(min, Math.min(max, value));
}

interface Props {
  current: string;
  onChange: (layout: string) => void;
}

export default function GridSelector({ current, onChange }: Props) {
  const custom = isCustomLayout(current);
  const parsed = custom ? { cols: 2, rows: 2 } : parseGrid(current);
  const [cols, setCols] = useState(clamp(parsed.cols || 2, MIN_COLS, MAX_COLS));
  const [rows, setRows] = useState(clamp(parsed.rows || 2, MIN_ROWS, MAX_ROWS));

  useEffect(() => {
    if (custom) return;
    const next = parseGrid(current);
    setCols(clamp(next.cols || 2, MIN_COLS, MAX_COLS));
    setRows(clamp(next.rows || 2, MIN_ROWS, MAX_ROWS));
  }, [current, custom]);

  const applyGrid = (nextCols: number, nextRows: number) => {
    const c = clamp(nextCols, MIN_COLS, MAX_COLS);
    const r = clamp(nextRows, MIN_ROWS, MAX_ROWS);
    setCols(c);
    setRows(r);
    onChange(`${c}x${r}`);
  };

  const adjustCols = (delta: number) => {
    applyGrid(cols + delta, rows);
  };

  const adjustRows = (delta: number) => {
    applyGrid(cols, rows + delta);
  };

  return (
    <div className="grid-selector">
      <div className="grid-layout-display">
        <span className="grid-layout-label">Layout</span>
        <span className="grid-layout-value">{custom ? "Custom" : `${cols}x${rows}`}</span>
      </div>

      <div className="grid-stepper-wrap">
        <div className="grid-stepper">
          <span className="grid-stepper-label">C</span>
          <button
            type="button"
            className="grid-stepper-btn"
            onClick={() => adjustCols(-1)}
            disabled={cols <= MIN_COLS}
            aria-label="Decrease columns"
          >
            -
          </button>
          <input
            className="grid-stepper-input"
            type="number"
            min={MIN_COLS}
            max={MAX_COLS}
            value={cols}
            onChange={(e) => {
              const next = Number(e.target.value);
              if (!Number.isFinite(next)) return;
              setCols(clamp(next, MIN_COLS, MAX_COLS));
            }}
            onBlur={() => applyGrid(cols, rows)}
          />
          <button
            type="button"
            className="grid-stepper-btn"
            onClick={() => adjustCols(1)}
            disabled={cols >= MAX_COLS}
            aria-label="Increase columns"
          >
            +
          </button>
        </div>

        <span className="grid-stepper-sep">x</span>

        <div className="grid-stepper">
          <span className="grid-stepper-label">R</span>
          <button
            type="button"
            className="grid-stepper-btn"
            onClick={() => adjustRows(-1)}
            disabled={rows <= MIN_ROWS}
            aria-label="Decrease rows"
          >
            -
          </button>
          <input
            className="grid-stepper-input"
            type="number"
            min={MIN_ROWS}
            max={MAX_ROWS}
            value={rows}
            onChange={(e) => {
              const next = Number(e.target.value);
              if (!Number.isFinite(next)) return;
              setRows(clamp(next, MIN_ROWS, MAX_ROWS));
            }}
            onBlur={() => applyGrid(cols, rows)}
          />
          <button
            type="button"
            className="grid-stepper-btn"
            onClick={() => adjustRows(1)}
            disabled={rows >= MAX_ROWS}
            aria-label="Increase rows"
          >
            +
          </button>
        </div>
      </div>

      <button
        type="button"
        className={`grid-custom-btn ${custom ? "grid-custom-btn-active" : ""}`}
        onClick={() => onChange(custom ? `${cols}x${rows}` : CUSTOM_LAYOUT)}
        title="Toggle custom mode"
      >
        Custom
      </button>
    </div>
  );
}
