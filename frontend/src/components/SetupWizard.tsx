import { useState, useEffect } from "react";
import { CLIType, Prompt } from "../lib/types";
import { useTerminals } from "../store/useTerminals";
import { OpenDirectoryDialog, ListPrompts } from "../../wailsjs/go/main/App";
import CLISelector from "./CLISelector";

interface Props {
  slotIndex: number;
  teamID: string;
  onCreated: (sessionID: string) => void;
}

export default function SetupWizard({ slotIndex, teamID, onCreated }: Props) {
  const { availableCLIs, addTerminal } = useTerminals();
  const [agentName, setAgentName] = useState("");
  const [selectedCLI, setSelectedCLI] = useState<CLIType>("shell");
  const [workDir, setWorkDir] = useState("");
  const [promptID, setPromptID] = useState("");
  const [prompts, setPrompts] = useState<Prompt[]>([]);
  const [creating, setCreating] = useState(false);

  // Set default CLI to first available AI CLI
  useEffect(() => {
    if (availableCLIs.length > 0) {
      const firstAI = availableCLIs.find((c) => c.available && c.type !== "shell");
      setSelectedCLI(firstAI ? (firstAI.type as CLIType) : "shell");
    }
  }, [availableCLIs]);

  // Load available prompts
  useEffect(() => {
    ListPrompts()
      .then((p) => setPrompts((p as unknown as Prompt[]) ?? []))
      .catch(() => {});
  }, []);

  const handleBrowse = async () => {
    try {
      const dir = await OpenDirectoryDialog();
      if (dir) setWorkDir(dir);
    } catch {
      // cancelled
    }
  };

  const handleCreate = async () => {
    if (creating) return;
    setCreating(true);
    try {
      const name = agentName.trim() || `agent-${slotIndex + 1}`;
      const sessionID = await addTerminal(teamID, name, workDir, selectedCLI, promptID);
      onCreated(sessionID);
    } catch (e) {
      console.error("Failed to create terminal:", e);
    } finally {
      setCreating(false);
    }
  };

  return (
    <div className="setup-wizard">
      <div className="setup-wizard-header">
        <span>Setup Terminal</span>
      </div>
      <div className="setup-wizard-body">
        <div className="setup-wizard-form">
          <div className="wizard-field">
            <label>Agent Name</label>
            <input
              type="text"
              value={agentName}
              onChange={(e) => setAgentName(e.target.value)}
              placeholder={`agent-${slotIndex + 1}`}
              onKeyDown={(e) => {
                if (e.key === "Enter") handleCreate();
              }}
            />
          </div>

          <div className="wizard-field">
            <label>CLI Type</label>
            <CLISelector
              availableCLIs={availableCLIs}
              selected={selectedCLI}
              onSelect={setSelectedCLI}
            />
          </div>

          <div className="wizard-field">
            <label>Workspace</label>
            <div className="wizard-dir-row">
              <input
                type="text"
                value={workDir}
                onChange={(e) => setWorkDir(e.target.value)}
                placeholder="Default directory"
                readOnly
              />
              <button type="button" className="btn-sm" onClick={handleBrowse}>
                Browse
              </button>
            </div>
          </div>

          {prompts.length > 0 && (
            <div className="wizard-field">
              <label>Startup Prompt <span className="wizard-optional">(optional)</span></label>
              <select
                value={promptID}
                onChange={(e) => setPromptID(e.target.value)}
              >
                <option value="">None</option>
                {prompts.map((p) => (
                  <option key={p.id} value={p.id}>
                    {p.name}
                  </option>
                ))}
              </select>
            </div>
          )}

          <button
            type="button"
            className="btn wizard-create-btn"
            onClick={handleCreate}
            disabled={creating}
          >
            {creating ? "Creating..." : "Create Terminal"}
          </button>
        </div>
      </div>
    </div>
  );
}
