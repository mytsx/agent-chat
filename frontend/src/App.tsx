import { useEffect, useState, useRef, useCallback, Component, ReactNode } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle, type PanelImperativeHandle } from "react-resizable-panels";
import { useTeams } from "./store/useTeams";
import { useMessages } from "./store/useMessages";
import { MessagesNewEvent, AgentsUpdatedEvent } from "./lib/types";
// useTerminals imported by PromptLibrary for target picker
import { SendPromptToAgent } from "../wailsjs/go/main/App";
import TabBar from "./components/TabBar";
import BroadcastBar from "./components/BroadcastBar";
import TerminalGrid from "./components/TerminalGrid";
import Sidebar from "./components/Sidebar";
import "./styles/globals.css";

class ErrorBoundary extends Component<
  { children: ReactNode },
  { error: string | null }
> {
  state = { error: null as string | null };

  static getDerivedStateFromError(error: Error) {
    return { error: error.message };
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{ padding: 20, color: "#f85149", fontFamily: "monospace" }}>
          <h2>Runtime Error</h2>
          <pre>{this.state.error}</pre>
        </div>
      );
    }
    return this.props.children;
  }
}

function AppContent() {
  const teams = useTeams((s) => s.teams);
  const activeTeamID = useTeams((s) => s.activeTeamID);
  const loadTeams = useTeams((s) => s.loadTeams);
  const createTeam = useTeams((s) => s.createTeam);
  const { addMessages, setAgents, loadMessages, loadAgents } = useMessages();
  const [ready, setReady] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const sidebarRef = useRef<PanelImperativeHandle>(null);
  const [worktreeNotice, setWorktreeNotice] = useState<{ agentName: string; worktreeDir: string } | null>(null);
  // Notifications that couldn't be injected into a terminal because the user kept
  // a pending input line past the deferral cap — surfaced here so they aren't
  // silently lost. A queue (not a single value) so concurrent deferrals from
  // multiple sessions don't overwrite each other (review C4).
  const [deferredNotices, setDeferredNotices] = useState<Array<{ id: number; agentName: string; prompt: string }>>([]);
  const deferredIdRef = useRef(0);

  const toggleSidebar = () => {
    if (sidebarRef.current) {
      if (sidebarCollapsed) {
        sidebarRef.current.expand();
      } else {
        sidebarRef.current.collapse();
      }
    }
  };

  // Load teams on startup (runs once)
  useEffect(() => {
    let cancelled = false;
    const init = async () => {
      try {
        await loadTeams();
        const currentTeams = useTeams.getState().teams;
        if (!cancelled && currentTeams.length === 0) {
          await createTeam("Default", "2x2", []);
        }
      } catch (e) {
        console.error("Failed to load teams:", e);
      }
      if (!cancelled) setReady(true);
    };
    init();
    return () => { cancelled = true; };
  }, []);

  // Set up event listeners for messages and agents
  useEffect(() => {
    if (!ready) return;

    let cleanupFn = () => {};

    import("../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      EventsOn("messages:new", (data: MessagesNewEvent) => {
        if (data?.chatDir && data?.messages) {
          addMessages(data.chatDir, data.messages);
        }
      });
      EventsOn("agents:updated", (data: AgentsUpdatedEvent) => {
        if (data?.chatDir && data?.agents) {
          setAgents(data.chatDir, data.agents);
        }
      });
      EventsOn("worktree:dirty", (data: { agentName?: string; worktreeDir?: string }) => {
        if (data?.agentName && data?.worktreeDir) {
          setWorktreeNotice({ agentName: data.agentName, worktreeDir: data.worktreeDir });
        }
      });
      EventsOn("notification:deferred", (data: { agentName?: string; prompt?: string }) => {
        if (data?.agentName) {
          const agentName = data.agentName;
          const prompt = data.prompt ?? "";
          // Increment the ref OUTSIDE the state updater: updaters must be pure, and
          // React StrictMode double-invokes them in dev (would skip IDs) — review G2.
          const nextId = deferredIdRef.current++;
          setDeferredNotices((prev) => [...prev, { id: nextId, agentName, prompt }]);
        }
      });
      cleanupFn = () => {
        try {
          EventsOff("messages:new");
          EventsOff("agents:updated");
          EventsOff("worktree:dirty");
          EventsOff("notification:deferred");
        } catch (e) {
          if (import.meta.env.DEV) console.warn("EventsOff cleanup failed:", e);
        }
      };
    }).catch((e) => {
      if (import.meta.env.DEV) console.warn("Failed to load Wails runtime:", e);
    });

    return () => cleanupFn();
  }, [ready]);

  // Load messages/agents when active team changes
  useEffect(() => {
    if (!activeTeamID) return;
    const team = useTeams.getState().teams.find((t) => t.id === activeTeamID);
    const roomName = team?.name || "default";
    loadMessages(roomName).catch((e) => {
      if (import.meta.env.DEV) console.warn("Failed to load messages:", e);
    });
    loadAgents(roomName).catch((e) => {
      if (import.meta.env.DEV) console.warn("Failed to load agents:", e);
    });
  }, [activeTeamID]);

  const activeTeam = teams.find((t) => t.id === activeTeamID);
  const chatDir = activeTeam?.name || "default";

  const dismissWorktreeNotice = useCallback(() => setWorktreeNotice(null), []);
  const dismissDeferredNotice = useCallback(
    (id: number) => setDeferredNotices((prev) => prev.filter((n) => n.id !== id)),
    []
  );

  const handleSendPrompt = (sessionID: string, content: string) => {
    SendPromptToAgent(sessionID, content, {}).catch((e) => {
      if (import.meta.env.DEV) console.warn("SendPromptToAgent failed:", e);
    });
  };

  return (
    <div className="app">
      {worktreeNotice && (
        <div className="worktree-notice">
          <span>
            <strong>{worktreeNotice.agentName}</strong> worktree&apos;si kirli olduğu için korunuyor: <code>{worktreeNotice.worktreeDir}</code>
          </span>
          <button type="button" onClick={dismissWorktreeNotice}>×</button>
        </div>
      )}
      {deferredNotices.map((notice) => (
        <div key={notice.id} className="deferred-notice" title={notice.prompt}>
          <span>
            🔔 <strong>{notice.agentName}</strong> için yeni mesaj bildirimi siz yazarken ertelendi ve terminale otomatik iletilemedi — agent&apos;a elle haber verebilir veya <code>read_messages</code> demesini bekleyebilirsiniz.
          </span>
          <button type="button" onClick={() => dismissDeferredNotice(notice.id)}>×</button>
        </div>
      ))}
      <TabBar />
      <BroadcastBar />
      <div className="app-body">
        <PanelGroup orientation="horizontal" className="app-panel-group">
          <Panel minSize="30%">
            <TerminalGrid />
          </Panel>
          <PanelResizeHandle className="resize-handle-sidebar">
            <button
              type="button"
              className="sidebar-toggle-btn"
              onClick={toggleSidebar}
              title={sidebarCollapsed ? "Show sidebar" : "Hide sidebar"}
            >
              {sidebarCollapsed ? "\u25C0" : "\u25B6"}
            </button>
          </PanelResizeHandle>
          <Panel
            panelRef={sidebarRef}
            collapsible
            defaultSize="20%"
            minSize="15%"
            maxSize="35%"
            onResize={(size) => setSidebarCollapsed(size.asPercentage === 0)}
          >
            <Sidebar chatDir={chatDir} onSendPrompt={handleSendPrompt} />
          </Panel>
        </PanelGroup>
      </div>
    </div>
  );
}

function App() {
  return (
    <ErrorBoundary>
      <AppContent />
    </ErrorBoundary>
  );
}

export default App;
