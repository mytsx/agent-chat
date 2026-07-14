// Go struct mirrors

export type CLIType = "claude" | "gemini" | "copilot" | "codex" | "shell";

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
  slot_index: number;
  use_worktree: boolean;
}

export interface Team {
  id: string;
  name: string;
  agents: AgentConfig[];
  grid_layout: string;
  chat_dir: string;
  manager_agent: string;
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
  original_to?: string;
  content: string;
  timestamp: string;
  type: string;
  routed_by_manager?: boolean;
  expects_reply: boolean;
  priority: string;
}

export interface Agent {
  role: string;
  joined_at: string;
  last_seen: number;
}

// Structured room metadata for the room browser (mirrors types.RoomSummary).
export interface RoomSummary {
  name: string;
  message_count: number;
  agents: Record<string, Agent>;
  historical_agents: string[];
  last_activity: string;
  is_default: boolean;
}

// A saved per-session room summary (#29, mirrors main.RoomSummaryInfo).
export interface RoomSummaryInfo {
  room: string;
  text: string;
  epoch: string;
  created_at: string;
  exists: boolean;
}

export interface SessionInfo {
  sessionID: string;
  cliType: CLIType;
  startUnix: number;
  lastUnix: number;
  durationSec: number;
  messageCount: number;
  snippet: string;
  fileMissing: boolean;
}

export interface TerminalSession {
  sessionID: string;
  teamID: string;
  agentName: string;
  cliType: CLIType;
  index: number;
  slotIndex: number;
  cliSessionID?: string; // captured CLI session ID for opt-in resume (#40)
}

// Wails event payload types
export interface MessagesNewEvent {
  chatDir: string;
  messages: Message[];
}

export interface AgentsUpdatedEvent {
  chatDir: string;
  agents: Record<string, Agent>;
}

// Grid layout type: "1x1" | "1x2" | "2x1" | "2x2" | "2x3" | "3x2" | "3x3" | "3x4" | "4x3" | "custom"
export type GridLayout = string;

export const CUSTOM_LAYOUT = "custom";

export function isCustomLayout(layout: string): boolean {
  return layout === CUSTOM_LAYOUT;
}

export function parseGrid(layout: string): { cols: number; rows: number } {
  if (isCustomLayout(layout)) return { cols: 1, rows: 1 };
  const [cols, rows] = layout.split("x").map(Number);
  return { cols: cols || 1, rows: rows || 1 };
}

export function gridCapacity(layout: string): number {
  if (isCustomLayout(layout)) return Infinity;
  const { cols, rows } = parseGrid(layout);
  return cols * rows;
}

// Voice / STT (#16)
export type VoiceState = "idle" | "recording" | "transcribing" | "error";

// Settings-panel view of voice config (mirrors main.VoiceStatus). Raw key never sent.
export interface VoiceStatus {
  hasKey: boolean;
  keyHint: string;
  ffmpegFound: boolean;
}

// voice:state:<sessionID> event payload (mirrors app.go emitVoiceState).
export interface VoiceStateEvent {
  state: VoiceState;
  message: string;
}

// voice:transcript:<sessionID> event payload — the transcribed text (UI feedback
// only; the text is already injected into the PTY by the backend).
export type VoiceTranscriptEvent = string;

// Usage / limits (#10, mirrors internal/usage.Status, .Window, .Snapshot)
export type UsageStatus = 0 | 1 | 2 | 3; // Unknown | OK | Warn | Critical

export interface UsageWindow {
  usedPercent: number;
  windowMinutes: number;
  resetsAt: number; // epoch sec; 0 = unknown
}

export interface UsageSnapshot {
  sessionID: string;
  agentName?: string;
  cli: CLIType;
  kind: number; // 0 none | 1 percentLimit | 2 tokenCount
  primary?: UsageWindow;
  secondary?: UsageWindow;
  planType?: string;
  inputTokens?: number;
  outputTokens?: number;
  cacheTokens?: number;
  model?: string;
  updatedAt: number;
}

// usage:updated event payload (mirrors app.go onUsage).
export interface UsageUpdatedEvent {
  snapshot: UsageSnapshot;
  status: UsageStatus;
}
