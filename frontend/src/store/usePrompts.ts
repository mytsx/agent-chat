import { create } from "zustand";
import { Prompt } from "../lib/types";
import {
  ListPrompts,
  CreatePrompt,
  UpdatePrompt,
  DeletePrompt,
} from "../../wailsjs/go/main/App";

interface PromptsState {
  prompts: Prompt[];
  loading: boolean;
  editorOpen: boolean;
  editingPrompt: Prompt | null;

  loadPrompts: () => Promise<void>;
  createPrompt: (
    name: string,
    content: string,
    category: string,
    tags: string[]
  ) => Promise<Prompt>;
  updatePrompt: (
    id: string,
    name: string,
    content: string,
    category: string,
    tags: string[]
  ) => Promise<void>;
  deletePrompt: (id: string) => Promise<void>;
  openEditor: (prompt?: Prompt) => void;
  closeEditor: () => void;
}

export const usePrompts = create<PromptsState>((set) => ({
  prompts: [],
  loading: false,
  editorOpen: false,
  editingPrompt: null,

  loadPrompts: async () => {
    set({ loading: true });
    try {
      const prompts = await ListPrompts();
      set({ prompts: prompts || [], loading: false });
    } catch {
      set({ loading: false });
    }
  },

  createPrompt: async (name, content, category, tags) => {
    const p = await CreatePrompt(name, content, category, tags);
    set((s) => ({ prompts: [...s.prompts, p] }));
    return p;
  },

  updatePrompt: async (id, name, content, category, tags) => {
    const p = await UpdatePrompt(id, name, content, category, tags);
    set((s) => ({
      prompts: s.prompts.map((pr) => (pr.id === id ? p : pr)),
    }));
  },

  deletePrompt: async (id) => {
    await DeletePrompt(id);
    set((s) => ({ prompts: s.prompts.filter((p) => p.id !== id) }));
  },

  openEditor: (prompt) =>
    set({ editorOpen: true, editingPrompt: prompt ?? null }),
  closeEditor: () => set({ editorOpen: false, editingPrompt: null }),
}));
