import { CUSTOM_LAYOUT } from "../lib/types";

const LAYOUTS = [
  { label: "1x1", cols: 1, rows: 1 },
  { label: "1x2", cols: 1, rows: 2 },
  { label: "2x1", cols: 2, rows: 1 },
  { label: "2x2", cols: 2, rows: 2 },
  { label: "2x3", cols: 2, rows: 3 },
  { label: "3x2", cols: 3, rows: 2 },
  { label: "3x3", cols: 3, rows: 3 },
  { label: "4x3", cols: 4, rows: 3 },
];

interface Props {
  current: string;
  onChange: (layout: string) => void;
}

export default function GridSelector({ current, onChange }: Props) {
  return (
    <div className="grid-selector">
      {LAYOUTS.map((l) => (
        <button
          key={l.label}
          className={`grid-option ${current === l.label ? "grid-option-active" : ""}`}
          onClick={() => onChange(l.label)}
          title={`${l.cols}x${l.rows} (${l.cols * l.rows} terminals)`}
        >
          <div
            className="grid-preview"
            style={{
              display: "grid",
              gridTemplateColumns: `repeat(${l.cols}, 1fr)`,
              gridTemplateRows: `repeat(${l.rows}, 1fr)`,
              gap: "1px",
              width: "24px",
              height: "18px",
            }}
          >
            {Array.from({ length: l.cols * l.rows }).map((_, i) => (
              <div key={i} className="grid-cell" />
            ))}
          </div>
          <span className="grid-label">{l.label}</span>
        </button>
      ))}
      <button
        className={`grid-option ${current === CUSTOM_LAYOUT ? "grid-option-active" : ""}`}
        onClick={() => onChange(CUSTOM_LAYOUT)}
        title="Custom (unlimited terminals)"
      >
        <span style={{ fontSize: "14px", lineHeight: 1 }}>+</span>
        <span className="grid-label">Custom</span>
      </button>
    </div>
  );
}
