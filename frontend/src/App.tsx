import { useEffect, useState, useRef, useCallback, Component, ReactNode } from "react";
import { Panel, Group as PanelGroup, Separator as PanelResizeHandle, type PanelImperativeHandle } from "react-resizable-panels";
import { useTeams } from "./store/useTeams";
import { useMessages } from "./store/useMessages";
import { useTerminals } from "./store/useTerminals";
import { useUsage } from "./store/useUsage";
import { useUpdate } from "./store/useUpdate";
import { MessagesNewEvent, AgentsUpdatedEvent, UsageUpdatedEvent, UpdateInfo } from "./lib/types";
import { errorToString } from "./lib/errorText";
import { SendPromptToAgent, GetPendingUpdate, ToggleFullscreen, GetAppVersion } from "../wailsjs/go/main/App";
import TabBar from "./components/TabBar";
import BroadcastBar from "./components/BroadcastBar";
import TerminalGrid from "./components/TerminalGrid";
import Sidebar from "./components/Sidebar";
import SettingsModal from "./components/SettingsModal";
import UpdateBanner from "./components/UpdateBanner";
import "./styles/globals.css";

class ErrorBoundary extends Component<
  { children: ReactNode },
  { error: string | null }
> {
  state = { error: null as string | null };

  static getDerivedStateFromError(error: unknown) {
    return { error: errorToString(error) };
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
  const [startupError, setStartupError] = useState<string | null>(null);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [showSettings, setShowSettings] = useState(false);
  const [appVersion, setAppVersion] = useState("");
  const sidebarRef = useRef<PanelImperativeHandle>(null);
  const [worktreeNotice, setWorktreeNotice] = useState<{ agentName: string; worktreeDir: string } | null>(null);
  // Notifications that couldn't be injected into a terminal because the user kept
  // a pending input line past the deferral cap — surfaced here so they aren't
  // silently lost. A queue (not a single value) so concurrent deferrals from
  // multiple sessions don't overwrite each other (review C4).
  const [deferredNotices, setDeferredNotices] = useState<Array<{ id: number; agentName: string; prompt: string }>>([]);
  const deferredIdRef = useRef(0);
  // Partial-broadcast advisories: some terminals got the broadcast, some didn't.
  // Non-fatal (the text still cleared on the rest), surfaced as a dismissable
  // queue so the user learns which agents were missed without losing their input.
  const [broadcastNotices, setBroadcastNotices] = useState<Array<{ id: number; injected: number; total: number; errors: string[] }>>([]);
  const broadcastIdRef = useRef(0);

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
        if (!cancelled) setStartupError(null);
      } catch (e) {
        console.error("Failed to load teams:", e);
        if (!cancelled) setStartupError(errorToString(e));
      }
      if (!cancelled) setReady(true);
    };
    init();
    return () => { cancelled = true; };
  }, []);

  // #83: the update banner is delivered over two independent channels so a lost event
  // can never hide it. (1) Attach the "update:available" listener on mount (NOT gated
  // on `ready`) so it's live before the Go startup check's network round-trip can emit.
  // (2) Also PULL any result the startup check already cached via GetPendingUpdate
  // (which makes NO network call) — this covers the race where the emit fired before
  // the listener was attached. setUpdate is idempotent, so both channels firing is fine.
  useEffect(() => {
    // `mounted` guards the async import: if the component unmounts before the dynamic
    // import resolves, the cleanup below has already run with `off` still a no-op, so we
    // must skip registering the listener entirely — otherwise it would leak (never
    // removed). Relevant under React StrictMode's mount→unmount→mount in dev.
    let mounted = true;
    let off = () => {};
    import("../wailsjs/runtime/runtime").then(({ EventsOn, EventsOff }) => {
      if (!mounted) return;
      EventsOn("update:available", (data: UpdateInfo) => {
        if (data?.version) useUpdate.getState().setUpdate(data);
      });
      off = () => {
        try {
          EventsOff("update:available");
        } catch (e) {
          if (import.meta.env.DEV) console.warn("update EventsOff failed:", e);
        }
      };
      // Pull AFTER the listener is live so the two channels can't both miss the result.
      GetPendingUpdate()
        .then((info) => {
          if (info?.version) useUpdate.getState().setUpdate(info);
        })
        .catch((e) => {
          if (import.meta.env.DEV) console.warn("GetPendingUpdate failed:", e);
        });
    }).catch((e) => {
      if (import.meta.env.DEV) console.warn("Failed to attach update listener:", e);
    });
    return () => {
      mounted = false;
      off();
    };
  }, []);

  // Fetch the embedded build version once for the subtle top-right badge (#UI).
  // Wrapped in try/catch: a Wails binding dereferences window.go synchronously, which
  // throws (not rejects) outside the Wails runtime (browser preview / tests) and would
  // otherwise crash the render — the .catch alone can't catch that synchronous throw.
  useEffect(() => {
    try {
      GetAppVersion()
        .then(setAppVersion)
        .catch(() => {});
    } catch (e) {
      if (import.meta.env.DEV) console.warn("GetAppVersion failed:", e);
    }
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
      EventsOn("broadcast:partial", (data: { injected?: number; total?: number; errors?: string[] }) => {
        const injected = data?.injected ?? 0;
        const total = data?.total ?? 0;
        const errors = data?.errors ?? [];
        if (errors.length === 0) return;
        // Increment the ref OUTSIDE the state updater (StrictMode double-invokes
        // updaters in dev), matching the deferred-notice handler.
        const nextId = broadcastIdRef.current++;
        setBroadcastNotices((prev) => [...prev, { id: nextId, injected, total, errors }]);
      });
      EventsOn("terminal:resume-available", (data: { sessionID: string; cliSessionID: string }) => {
        useTerminals.getState().setCLISessionID(data.sessionID, data.cliSessionID);
      });
      EventsOn("usage:updated", (data: UsageUpdatedEvent) => {
        if (!data?.snapshot) return;
        useUsage.getState().applySnapshot(data);
      });
      EventsOn("usage:limit-hit", (data: { sessionID: string }) => {
        useUsage.getState().markLimitHit(data.sessionID);
      });
      cleanupFn = () => {
        try {
          EventsOff("messages:new");
          EventsOff("agents:updated");
          EventsOff("worktree:dirty");
          EventsOff("notification:deferred");
          EventsOff("broadcast:partial");
          EventsOff("terminal:resume-available");
          EventsOff("usage:updated");
          EventsOff("usage:limit-hit");
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
  const dismissBroadcastNotice = useCallback(
    (id: number) => setBroadcastNotices((prev) => prev.filter((n) => n.id !== id)),
    []
  );

  const handleSendPrompt = async (sessionID: string, content: string) => {
    try {
      await SendPromptToAgent(sessionID, content, {});
    } catch (e) {
      if (import.meta.env.DEV) console.warn("SendPromptToAgent failed:", e);
      throw new Error(errorToString(e));
    }
  };

  return (
    <div className="app">
      {showSettings && <SettingsModal onClose={() => setShowSettings(false)} appVersion={appVersion} />}
      <UpdateBanner />
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
      {broadcastNotices.map((notice) => (
        <div
          key={notice.id}
          className="broadcast-notice"
          title={notice.errors.join("\n")}
        >
          <span>
            📢 Toplu mesaj <strong>{notice.injected}/{notice.total}</strong> terminale iletildi — {notice.errors.length} terminale ulaşmadı: <code>{notice.errors.map((e) => e.split(":")[0]).join(", ")}</code>
          </span>
          <button type="button" onClick={() => dismissBroadcastNotice(notice.id)}>×</button>
        </div>
      ))}
      {/* Controls live in a relative row WITH the TabBar (not position:fixed to the
          viewport) so they flow down together when the UpdateBanner/notices push the
          header down, instead of hanging over the banner (review). */}
      <div className="app-header-row">
        <TabBar />
        {appVersion && (
          <span className="app-version-badge" title="Uygulama sürümü">
            {/^\d/.test(appVersion) ? `v${appVersion}` : appVersion}
          </span>
        )}
        <button
          type="button"
          className="app-fullscreen-btn"
          onClick={() => {
            // try/catch: the binding throws synchronously outside the Wails runtime
            // (browser preview / tests); .catch alone can't catch that. In the real app
            // it always succeeds, so no user-facing alert is warranted.
            try {
              ToggleFullscreen().catch((e) => {
                if (import.meta.env.DEV) console.warn("ToggleFullscreen failed:", e);
              });
            } catch (e) {
              if (import.meta.env.DEV) console.warn("ToggleFullscreen failed:", e);
            }
          }}
          title="Tam ekran (aç/kapat)"
          aria-label="Tam ekran"
        >
          ⛶
        </button>
        <button
          type="button"
          className="app-settings-btn"
          onClick={() => setShowSettings(true)}
          title="Ayarlar (sesli prompt / API anahtarı)"
        >
          ⚙️
        </button>
      </div>
      <BroadcastBar />
      {startupError && (
        <div className="broadcast-notice" title={startupError}>
          <span>
            ⚠️ Başlangıç oda kurulumu tamamlanamadı. Backend bağlantısını kontrol edip tekrar deneyin: <code>{startupError}</code>
          </span>
          <button type="button" onClick={() => setStartupError(null)}>×</button>
        </div>
      )}
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
