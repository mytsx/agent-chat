import { CLIInfo, CLIType } from "../lib/types";

const CLI_COLORS: Record<CLIType, string> = {
  claude: "#d2a8ff",
  gemini: "#56d364",
  copilot: "#58a6ff",
  codex: "#f7a32b",
  shell: "#8b949e",
};

interface Props {
  availableCLIs: CLIInfo[];
  selected: CLIType;
  onSelect: (type: CLIType) => void;
}

export default function CLISelector({ availableCLIs, selected, onSelect }: Props) {
  return (
    <div className="wizard-cli-group">
      {availableCLIs.map((c) => {
        const cliType = c.type as CLIType;
        const isSelected = selected === cliType;
        const color = CLI_COLORS[cliType] || "#8b949e";

        return (
          <button
            key={c.type}
            type="button"
            className={`wizard-cli-btn ${isSelected ? `wizard-cli-btn-active` : ""}`}
            style={
              isSelected
                ? { borderColor: color, background: `${color}20` }
                : undefined
            }
            disabled={!c.available}
            onClick={() => onSelect(cliType)}
            title={c.available ? c.name : `${c.name} (not installed)`}
          >
            {isSelected && <span className="wizard-cli-check">&#10003;</span>}
            {c.name}
          </button>
        );
      })}
    </div>
  );
}
