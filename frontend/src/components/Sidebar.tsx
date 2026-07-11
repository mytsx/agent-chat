import { useState } from "react";
import AgentStatus from "./AgentStatus";
import MessageFeed from "./MessageFeed";
import PromptLibrary from "./PromptLibrary";
import RoomBrowser from "./RoomBrowser";

interface Props {
  chatDir: string;
  onSendPrompt?: (sessionID: string, content: string) => Promise<void>;
}

type SidebarTab = "status" | "messages" | "prompts" | "rooms";

const SIDEBAR_TABS: { id: SidebarTab; label: string }[] = [
  { id: "status", label: "Agents" },
  { id: "messages", label: "Messages" },
  { id: "prompts", label: "Prompts" },
  { id: "rooms", label: "Rooms" },
];

export default function Sidebar({ chatDir, onSendPrompt }: Props) {
  const [activeTab, setActiveTab] = useState<SidebarTab>("messages");

  return (
    <div className="sidebar">
      <div className="sidebar-tabs" role="tablist" aria-label="Sidebar sections">
        {SIDEBAR_TABS.map((tab) => (
          <button
            key={tab.id}
            id={`sidebar-tab-${tab.id}`}
            type="button"
            role="tab"
            aria-selected={activeTab === tab.id}
            aria-controls={`sidebar-panel-${tab.id}`}
            className={`sidebar-tab ${activeTab === tab.id ? "sidebar-tab-active" : ""}`}
            onClick={() => setActiveTab(tab.id)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      <div
        id={`sidebar-panel-${activeTab}`}
        className="sidebar-content"
        role="tabpanel"
        aria-labelledby={`sidebar-tab-${activeTab}`}
      >
        {activeTab === "status" && <AgentStatus chatDir={chatDir} />}
        {activeTab === "messages" && <MessageFeed chatDir={chatDir} />}
        {activeTab === "prompts" && (
          <PromptLibrary onSendPrompt={onSendPrompt} />
        )}
        {activeTab === "rooms" && <RoomBrowser />}
      </div>
    </div>
  );
}
