// Go struct mirrors

export type CLIType = "claude" | "gemini" | "copilot" | "shell";

export interface CLIInfo {
  type: CLIType;
  name: string;
  binary: string;
  available: boolean;
  binary_path: string;
}

export interface AgentConfig {
  name: string;
  role: string;
  prompt_id: string;
  work_dir: string;
  cli_type: string;
}

export interface Team {
  id: string;
  name: string;
  agents: AgentConfig[];
  grid_layout: string;
  chat_dir: string;
  custom_prompt: string;
  created_at: string;
}

export interface Prompt {
  id: string;
  name: string;
  content: string;
  category: string;
  tags: string[];
  variables: string[];
  created_at: string;
  updated_at: string;
}

export interface Message {
  id: number;
  from: string;
  to: string;
  content: string;
  timestamp: string;
  type: string;
  expects_reply: boolean;
  priority: string;
}

export interface Agent {
  role: string;
  joined_at: string;
  last_seen: number;
}

export interface TerminalSession {
  sessionID: string;
  teamID: string;
  agentName: string;
  cliType: CLIType;
  index: number;
}

// Grid layout type: "1x1" | "1x2" | "2x1" | "2x2" | "2x3" | "3x2" | "3x3" | "3x4" | "4x3"
export type GridLayout = string;

export function parseGrid(layout: string): { cols: number; rows: number } {
  const [cols, rows] = layout.split("x").map(Number);
  return { cols: cols || 1, rows: rows || 1 };
}

export function gridCapacity(layout: string): number {
  const { cols, rows } = parseGrid(layout);
  return cols * rows;
}
