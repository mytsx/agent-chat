import { useState, useEffect } from "react";
import { CLIType, SessionInfo } from "../lib/types";
import { useTerminals } from "../store/useTerminals";
import { usePrompts } from "../store/usePrompts";
import { useTeams } from "../store/useTeams";
import { OpenDirectoryDialog, IsGitRepo } from "../../wailsjs/go/main/App";
import CLISelector from "./CLISelector";

interface Props {
  slotIndex: number;
  teamID: string;
  onCreated: (sessionID: string) => void;
}

export default function SetupWizard({ slotIndex, teamID, onCreated }: Props) {
  const { availableCLIs, addTerminal, listKnownAgents, listAgentSessions, createTerminalResume } = useTerminals();
  const setTeamManager = useTeams((s) => s.setTeamManager);
  const setTeamObserver = useTeams((s) => s.setTeamObserver);
  const prompts = usePrompts((s) => s.prompts);
  const [agentName, setAgentName] = useState("");
  const [selectedCLI, setSelectedCLI] = useState<CLIType>("shell");
  const [workDir, setWorkDir] = useState("");
  const [promptID, setPromptID] = useState("");
  const [setAsManager, setSetAsManager] = useState(false);
  const [setAsObserver, setSetAsObserver] = useState(false);
  const [useWorktree, setUseWorktree] = useState(false);
  const [isGitRepoDir, setIsGitRepoDir] = useState(false);
  const [creating, setCreating] = useState(false);
  const [knownAgents, setKnownAgents] = useState<string[]>([]);
  const [agentSessions, setAgentSessions] = useState<SessionInfo[]>([]);
  const [resumeID, setResumeID] = useState("");

  // Load known agents on mount for autocomplete
  useEffect(() => {
    listKnownAgents(teamID).then(setKnownAgents).catch(() => {});
  }, [teamID]);

  // Set default CLI to first available AI CLI
  useEffect(() => {
    if (availableCLIs.length > 0) {
      const firstAI = availableCLIs.find((c) => c.available && c.type !== "shell");
      setSelectedCLI(firstAI ? (firstAI.type as CLIType) : "shell");
    }
  }, [availableCLIs]);

  // Check if workDir is a git repo
  useEffect(() => {
    if (!workDir) {
      setIsGitRepoDir(false);
      setUseWorktree(false);
      return;
    }
    IsGitRepo(workDir).then((result) => {
      setIsGitRepoDir(result);
      if (!result) setUseWorktree(false);
    }).catch(() => {
      setIsGitRepoDir(false);
      setUseWorktree(false);
    });
  }, [workDir]);

  // Fetch this agent's resumable sessions when the name settles. DEBOUNCED (300ms):
  // ListAgentSessions stats+parses transcript files, so firing per keystroke would
  // thrash disk I/O and the UI (Gemini). An `active` guard drops a stale response so a
  // slow earlier fetch can't overwrite a newer name's sessions and let handleCreate
  // submit a resumeID belonging to a different agent (Codex P2).
  useEffect(() => {
    const trimmed = agentName.trim();
    setResumeID(""); // any in-flight pick is invalid once the name/CLI changes
    // Clear the prior agent/CLI's sessions IMMEDIATELY (not after the debounce) — a row
    // left visible during the 300ms fetch window could be picked and then submitted under
    // the new agent/CLI by handleCreate (Codex P2).
    setAgentSessions([]);
    if (!trimmed) {
      return;
    }
    let active = true;
    const timer = setTimeout(() => {
      listAgentSessions(teamID, trimmed).then((sessions) => {
        // Only resumable-as-configured sessions: the picker resumes under selectedCLI,
        // so a Claude session under a now-Codex selection can't be opened (Codex P2).
        if (active) setAgentSessions(sessions.filter((s) => s.cliType === selectedCLI));
      }).catch((err) => {
        // Background autocomplete: log but don't alert per-keystroke (would spam); an
        // empty list degrades gracefully to "no past sessions".
        console.error("[SetupWizard] listAgentSessions failed:", err);
        if (active) setAgentSessions([]);
      });
    }, 300);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [agentName, teamID, selectedCLI]);

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
      // Persist the role BEFORE creating the terminal: CreateTerminal's
      // resolveAgentMode reads it back to compose the right startup prompt and
      // skip worktree/orchestrator. Manager and observer are mutually exclusive.
      if (setAsManager) {
        await setTeamManager(teamID, name);
      } else if (setAsObserver) {
        await setTeamObserver(teamID, name);
      }
      let sessionID: string;
      if (resumeID) {
        sessionID = await createTerminalResume(teamID, name, workDir, selectedCLI, promptID, slotIndex, useWorktree, resumeID);
      } else {
        sessionID = await addTerminal(teamID, name, workDir, selectedCLI, promptID, slotIndex, useWorktree);
      }
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
              list={`agent-history-${teamID}-${slotIndex}`}
              onKeyDown={(e) => {
                if (e.key === "Enter") handleCreate();
              }}
            />
            {/* Per-instance id: SetupWizard renders once per empty slot, so a fixed
                datalist id would collide and drop suggestions in the duplicates (Copilot). */}
            <datalist id={`agent-history-${teamID}-${slotIndex}`}>
              {knownAgents.map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
          </div>

          {agentSessions.length > 0 && (
            <div className="wizard-field">
              <label>Oturum <span className="wizard-optional">(geçmişten devam)</span></label>
              <select value={resumeID} onChange={(e) => setResumeID(e.target.value)}>
                <option value="">✨ Yeni (taze)</option>
                {agentSessions.map((s) => (
                  // fileMissing: shown (so the user sees why it's unavailable) but disabled
                  // — its transcript is pruned, so resuming would open an error/fresh
                  // terminal; matches the bulk picker paths that skip it (Codex P2).
                  <option key={s.sessionID} value={s.sessionID} disabled={s.fileMissing}>
                    {new Date(s.startUnix * 1000).toLocaleString("tr-TR", { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" })} · {s.messageCount} mesaj{s.fileMissing ? " ⚠️ dosya yok (devam edilemez)" : ""}
                  </option>
                ))}
              </select>
            </div>
          )}

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

          <div className="wizard-field">
            <label className="wizard-checkbox-row">
              <input
                type="checkbox"
                checked={setAsManager}
                onChange={(e) => {
                  setSetAsManager(e.target.checked);
                  if (e.target.checked) setSetAsObserver(false);
                }}
              />
              <span>Set as manager</span>
            </label>
          </div>

          <div className="wizard-field">
            <label className="wizard-checkbox-row">
              <input
                type="checkbox"
                checked={setAsObserver}
                onChange={(e) => {
                  setSetAsObserver(e.target.checked);
                  if (e.target.checked) setSetAsManager(false);
                }}
              />
              <span>Set as observer (read-only)</span>
            </label>
            {setAsObserver && (
              <span className="wizard-hint">
                Gözlemci yalnızca odayı izler; mesaj gönderemez, sadece seninle konuşur
              </span>
            )}
          </div>

          {isGitRepoDir && (
            <div className="wizard-field">
              <label className="wizard-checkbox-row">
                <input
                  type="checkbox"
                  checked={useWorktree}
                  onChange={(e) => setUseWorktree(e.target.checked)}
                  disabled={setAsManager || setAsObserver}
                />
                <span>Use Git Worktree</span>
              </label>
              {setAsManager && (
                <span className="wizard-hint">Manager agent ana repoda çalışır</span>
              )}
              {setAsObserver && (
                <span className="wizard-hint">Gözlemci agent ana repoda çalışır</span>
              )}
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
