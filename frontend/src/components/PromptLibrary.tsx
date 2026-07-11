import { useEffect, useState } from "react";
import { usePrompts } from "../store/usePrompts";
import { useTerminals } from "../store/useTerminals";
import { useTeams } from "../store/useTeams";
import { TerminalSession } from "../lib/types";
import { errorToString } from "../lib/errorText";
import PromptEditor from "./PromptEditor";

interface Props {
  onSendPrompt?: (sessionID: string, content: string) => Promise<void>;
}

export default function PromptLibrary({ onSendPrompt }: Props) {
  const { prompts, loading, loadPrompts, deletePrompt, openEditor, editorOpen } =
    usePrompts();
  const activeTeamID = useTeams((s) => s.activeTeamID);
  const sessions = useTerminals((s) => s.sessions);
  const [sendingPromptId, setSendingPromptId] = useState<string | null>(null);
  const [sendingTargetID, setSendingTargetID] = useState<string | null>(null);
  const [sendError, setSendError] = useState<string | null>(null);

  const teamSessions: TerminalSession[] =
    activeTeamID ? (sessions[activeTeamID] ?? []) : [];

  useEffect(() => {
    loadPrompts();
  }, []);

  const handleSend = async (promptContent: string, session: TerminalSession) => {
    if (!onSendPrompt || sendingTargetID) return;
    setSendingTargetID(session.sessionID);
    setSendError(null);
    try {
      await onSendPrompt(session.sessionID, promptContent);
      setSendingPromptId(null);
    } catch (e) {
      setSendError(errorToString(e));
    } finally {
      setSendingTargetID(null);
    }
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
                    {sendError && (
                      <span className="prompt-send-error" role="alert" title={sendError}>
                        Gönderilemedi: {sendError}
                      </span>
                    )}
                    {teamSessions.length === 0 ? (
                      <span className="picker-empty">No terminals</span>
                    ) : (
                      teamSessions.map((s) => (
                        <button
                          key={s.sessionID}
                          className="btn-sm btn-target"
                          onClick={() => void handleSend(p.content, s)}
                          disabled={!!sendingTargetID}
                        >
                          {sendingTargetID === s.sessionID
                            ? "Sending…"
                            : s.agentName || `Terminal ${s.index + 1}`}
                        </button>
                      ))
                    )}
                    <button
                      className="btn-sm"
                      onClick={() => {
                        setSendingPromptId(null);
                        setSendError(null);
                      }}
                      disabled={!!sendingTargetID}
                    >
                      Cancel
                    </button>
                  </div>
                ) : (
                  <>
                    <button
                      className="btn-sm"
                      onClick={() => {
                        setSendError(null);
                        setSendingPromptId(p.id);
                      }}
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
