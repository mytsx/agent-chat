import { create } from "zustand";
import { Message, Agent } from "../lib/types";
import { GetMessages, GetAgents } from "../../wailsjs/go/main/App";

// Stable empty references to avoid infinite re-render loops
const EMPTY_MESSAGES: Message[] = [];
const EMPTY_AGENTS: Record<string, Agent> = {};

interface MessagesState {
  messages: Record<string, Message[]>;
  agents: Record<string, Record<string, Agent>>;

  addMessages: (chatDir: string, newMessages: Message[]) => void;
  setAgents: (chatDir: string, agents: Record<string, Agent>) => void;
  loadMessages: (chatDir: string) => Promise<void>;
  loadAgents: (chatDir: string) => Promise<void>;
}

export const useMessages = create<MessagesState>((set) => ({
  messages: {},
  agents: {},

  addMessages: (chatDir, newMessages) => {
    set((s) => {
      const existing = s.messages[chatDir] ?? EMPTY_MESSAGES;
      const existingIDs = new Set(existing.map((m) => m.id));
      const uniqueNew = newMessages.filter((m) => !existingIDs.has(m.id));
      if (uniqueNew.length === 0) return s;
      return {
        messages: {
          ...s.messages,
          [chatDir]: [...existing, ...uniqueNew],
        },
      };
    });
  },

  setAgents: (chatDir, agents) => {
    set((s) => ({
      agents: { ...s.agents, [chatDir]: agents },
    }));
  },

  loadMessages: async (chatDir) => {
    try {
      const msgs = await GetMessages(chatDir);
      set((s) => ({
        messages: { ...s.messages, [chatDir]: msgs || EMPTY_MESSAGES },
      }));
    } catch {
      // ignore
    }
  },

  loadAgents: async (chatDir) => {
    try {
      const agents = await GetAgents(chatDir);
      set((s) => ({
        agents: { ...s.agents, [chatDir]: agents || EMPTY_AGENTS },
      }));
    } catch {
      // ignore
    }
  },
}));

// Selector hooks with stable references
export function useMessagesFor(chatDir: string): Message[] {
  return useMessages((s) => s.messages[chatDir] ?? EMPTY_MESSAGES);
}

export function useAgentsFor(chatDir: string): Record<string, Agent> {
  return useMessages((s) => s.agents[chatDir] ?? EMPTY_AGENTS);
}
