import { useEffect, useState, Component, ReactNode } from "react";
import { useTeams } from "./store/useTeams";
import { useMessages } from "./store/useMessages";
// useTerminals imported by PromptLibrary for target picker
import { SendPromptToAgent } from "../wailsjs/go/main/App";
import TabBar from "./components/TabBar";
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
      EventsOn("messages:new", (data: any) => {
        if (data?.chatDir && data?.messages) {
          addMessages(data.chatDir, data.messages);
        }
      });
      EventsOn("agents:updated", (data: any) => {
        if (data?.chatDir && data?.agents) {
          setAgents(data.chatDir, data.agents);
        }
      });
      cleanupFn = () => {
        try {
          EventsOff("messages:new");
          EventsOff("agents:updated");
        } catch {}
      };
    }).catch(() => {});

    return () => cleanupFn();
  }, [ready]);

  // Load messages/agents when active team changes
  useEffect(() => {
    if (!activeTeamID) return;
    const team = useTeams.getState().teams.find((t) => t.id === activeTeamID);
    if (team?.chat_dir) {
      const roomDir = team.chat_dir + "/" + (team.name || "default");
      loadMessages(roomDir).catch(() => {});
      loadAgents(roomDir).catch(() => {});
    }
  }, [activeTeamID]);

  const activeTeam = teams.find((t) => t.id === activeTeamID);
  const chatDir = activeTeam
    ? activeTeam.chat_dir + "/" + (activeTeam.name || "default")
    : "/tmp/agent-chat-room/default";

  const handleSendPrompt = (sessionID: string, content: string) => {
    SendPromptToAgent(sessionID, content, {}).catch(() => {});
  };

  return (
    <div className="app">
      <TabBar />
      <div className="app-body">
        <TerminalGrid />
        <Sidebar chatDir={chatDir} onSendPrompt={handleSendPrompt} />
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
