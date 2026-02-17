import { useEffect, useState } from "react";
import { usePrompts } from "../store/usePrompts";
import { useTerminals } from "../store/useTerminals";
import { useTeams } from "../store/useTeams";
import { TerminalSession } from "../lib/types";
import PromptEditor from "./PromptEditor";

interface Props {
  onSendPrompt?: (sessionID: string, content: string) => void;
}

export default function PromptLibrary({ onSendPrompt }: Props) {
  const { prompts, loading, loadPrompts, deletePrompt, openEditor, editorOpen } =
    usePrompts();
  const activeTeamID = useTeams((s) => s.activeTeamID);
  const sessions = useTerminals((s) => s.sessions);
  const [sendingPromptId, setSendingPromptId] = useState<string | null>(null);

  const teamSessions: TerminalSession[] =
    activeTeamID ? (sessions[activeTeamID] ?? []) : [];

  useEffect(() => {
    loadPrompts();
  }, []);

  const handleSend = (promptContent: string, session: TerminalSession) => {
    onSendPrompt?.(session.sessionID, promptContent);
    setSendingPromptId(null);
  };

  return (
    <div className="prompt-library">
      <div className="sidebar-section-header">
        <h3 className="sidebar-section-title">Prompts</h3>
        <button className="btn-sm" onClick={() => openEditor()}>
          + New
        </button>
      </div>

      {loading ? (
        <p className="sidebar-empty">Loading...</p>
      ) : prompts.length === 0 ? (
        <p className="sidebar-empty">No prompts saved</p>
      ) : (
        <div className="prompt-list">
          {prompts.map((p) => (
            <div key={p.id} className="prompt-card">
              <div className="prompt-card-header">
                <span className="prompt-name">{p.name}</span>
                <span className={`prompt-category cat-${p.category}`}>
                  {p.category}
                </span>
              </div>
              <p className="prompt-preview">
                {p.content.substring(0, 100)}
                {p.content.length > 100 ? "..." : ""}
              </p>
              {p.variables && p.variables.length > 0 && (
                <div className="prompt-vars">
                  {p.variables.map((v) => (
                    <span key={v} className="prompt-var">
                      {`{{${v}}}`}
                    </span>
                  ))}
                </div>
              )}
              <div className="prompt-actions">
                {sendingPromptId === p.id ? (
                  <div className="prompt-target-picker">
                    <span className="picker-label">Send to:</span>
                    {teamSessions.length === 0 ? (
                      <span className="picker-empty">No terminals</span>
                    ) : (
                      teamSessions.map((s) => (
                        <button
                          key={s.sessionID}
                          className="btn-sm btn-target"
                          onClick={() => handleSend(p.content, s)}
                        >
                          {s.agentName || `Terminal ${s.index + 1}`}
                        </button>
                      ))
                    )}
                    <button
                      className="btn-sm"
                      onClick={() => setSendingPromptId(null)}
                    >
                      Cancel
                    </button>
                  </div>
                ) : (
                  <>
                    <button
                      className="btn-sm"
                      onClick={() => setSendingPromptId(p.id)}
                    >
                      Send
                    </button>
                    <button className="btn-sm" onClick={() => openEditor(p)}>
                      Edit
                    </button>
                    <button
                      className="btn-sm btn-danger"
                      onClick={() => deletePrompt(p.id)}
                    >
                      Delete
                    </button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {editorOpen && <PromptEditor />}
    </div>
  );
}
