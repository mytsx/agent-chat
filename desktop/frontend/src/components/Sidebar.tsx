import { useState } from "react";
import AgentStatus from "./AgentStatus";
import MessageFeed from "./MessageFeed";
import PromptLibrary from "./PromptLibrary";

interface Props {
  chatDir: string;
  onSendPrompt?: (sessionID: string, content: string) => void;
}

type SidebarTab = "status" | "messages" | "prompts";

export default function Sidebar({ chatDir, onSendPrompt }: Props) {
  const [activeTab, setActiveTab] = useState<SidebarTab>("messages");

  return (
    <div className="sidebar">
      <div className="sidebar-tabs">
        <button
          className={`sidebar-tab ${activeTab === "status" ? "sidebar-tab-active" : ""}`}
          onClick={() => setActiveTab("status")}
        >
          Agents
        </button>
        <button
          className={`sidebar-tab ${activeTab === "messages" ? "sidebar-tab-active" : ""}`}
          onClick={() => setActiveTab("messages")}
        >
          Messages
        </button>
        <button
          className={`sidebar-tab ${activeTab === "prompts" ? "sidebar-tab-active" : ""}`}
          onClick={() => setActiveTab("prompts")}
        >
          Prompts
        </button>
      </div>

      <div className="sidebar-content">
        {activeTab === "status" && <AgentStatus chatDir={chatDir} />}
        {activeTab === "messages" && <MessageFeed chatDir={chatDir} />}
        {activeTab === "prompts" && (
          <PromptLibrary onSendPrompt={onSendPrompt} />
        )}
      </div>
    </div>
  );
}
