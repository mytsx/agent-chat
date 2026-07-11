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
  const [confirmDeletePromptId, setConfirmDeletePromptId] = useState<string | null>(null);
  const [deletingPromptId, setDeletingPromptId] = useState<string | null>(null);
  const [deleteError, setDeleteError] = useState<string | null>(null);

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

  const handleDelete = async (promptId: string) => {
    if (deletingPromptId) return;
    setDeletingPromptId(promptId);
    setDeleteError(null);
    try {
      await deletePrompt(promptId);
      setConfirmDeletePromptId(null);
    } catch (e) {
      setDeleteError(errorToString(e));
    } finally {
      setDeletingPromptId(null);
    }
  };

  return (
    <div className="prompt-library">
      <div className="sidebar-section-header">
        <h3 className="sidebar-section-title">Prompts</h3>
        <button className="btn-sm" type="button" onClick={() => openEditor()}>
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
                          type="button"
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
                      type="button"
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
                      type="button"
                      className="btn-sm"
                      onClick={() => {
                        setSendError(null);
                        setConfirmDeletePromptId(null);
                        setDeleteError(null);
                        setSendingPromptId(p.id);
                      }}
                    >
                      Send
                    </button>
                    <button className="btn-sm" type="button" onClick={() => openEditor(p)}>
                      Edit
                    </button>
                    {confirmDeletePromptId === p.id ? (
                      <>
                        <button
                          type="button"
                          className="btn-sm btn-danger"
                          onClick={() => void handleDelete(p.id)}
                          disabled={!!deletingPromptId}
                          aria-label={`${p.name} promptunu silmeyi onayla`}
                        >
                          {deletingPromptId === p.id ? "Deleting…" : "Confirm delete"}
                        </button>
                        <button
                          type="button"
                          className="btn-sm"
                          onClick={() => {
                            setConfirmDeletePromptId(null);
                            setDeleteError(null);
                          }}
                          disabled={!!deletingPromptId}
                        >
                          Cancel
                        </button>
                      </>
                    ) : (
                      <button
                        type="button"
                        className="btn-sm btn-danger"
                        onClick={() => {
                          setSendingPromptId(null);
                          setSendError(null);
                          setDeleteError(null);
                          setConfirmDeletePromptId(p.id);
                        }}
                        aria-label={`${p.name} promptunu sil`}
                      >
                        Delete
                      </button>
                    )}
                  </>
                )}
              </div>
              {confirmDeletePromptId === p.id && !deletingPromptId && (
                <p className="prompt-delete-hint" role="status">
                  This deletes “{p.name}”. Confirm to continue.
                </p>
              )}
              {confirmDeletePromptId === p.id && deleteError && (
                <p className="prompt-send-error" role="alert">
                  Delete failed: {deleteError}
                </p>
              )}
            </div>
          ))}
        </div>
      )}

      {editorOpen && <PromptEditor />}
    </div>
  );
}
