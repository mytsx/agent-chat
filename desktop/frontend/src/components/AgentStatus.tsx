import { useAgentsFor } from "../store/useMessages";
import { Agent } from "../lib/types";

interface Props {
  chatDir: string;
}

export default function AgentStatus({ chatDir }: Props) {
  const agents = useAgentsFor(chatDir);

  const entries = Object.entries(agents);

  if (entries.length === 0) {
    return (
      <div className="agent-status">
        <h3 className="sidebar-section-title">Agents</h3>
        <p className="sidebar-empty">No agents in room</p>
      </div>
    );
  }

  const isActive = (agent: Agent) => {
    return Date.now() / 1000 - agent.last_seen < 300;
  };

  return (
    <div className="agent-status">
      <h3 className="sidebar-section-title">
        Agents ({entries.length})
      </h3>
      <div className="agent-list">
        {entries.map(([name, agent]) => (
          <div key={name} className="agent-card">
            <span
              className={`agent-indicator ${isActive(agent) ? "agent-active" : "agent-offline"}`}
            />
            <div className="agent-info">
              <span className="agent-name">{name}</span>
              {agent.role && (
                <span className="agent-role">{agent.role}</span>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  );
}
