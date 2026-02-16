import { useState, useEffect } from "react";
import { usePrompts } from "../store/usePrompts";

export default function PromptEditor() {
  const { editingPrompt, closeEditor, createPrompt, updatePrompt } =
    usePrompts();

  const [name, setName] = useState("");
  const [content, setContent] = useState("");
  const [category, setCategory] = useState("task");
  const [tags, setTags] = useState("");

  useEffect(() => {
    if (editingPrompt) {
      setName(editingPrompt.name);
      setContent(editingPrompt.content);
      setCategory(editingPrompt.category);
      setTags(editingPrompt.tags?.join(", ") ?? "");
    }
  }, [editingPrompt]);

  const handleSave = async () => {
    if (!name.trim() || !content.trim()) return;

    const tagList = tags
      .split(",")
      .map((t) => t.trim())
      .filter(Boolean);

    if (editingPrompt) {
      await updatePrompt(editingPrompt.id, name, content, category, tagList);
    } else {
      await createPrompt(name, content, category, tagList);
    }
    closeEditor();
  };

  return (
    <div className="modal-overlay" onClick={closeEditor}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <h3>{editingPrompt ? "Edit Prompt" : "New Prompt"}</h3>

        <div className="form-group">
          <label>Name</label>
          <input value={name} onChange={(e) => setName(e.target.value)} />
        </div>

        <div className="form-group">
          <label>Category</label>
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="role">Role</option>
            <option value="task">Task</option>
            <option value="system">System</option>
          </select>
        </div>

        <div className="form-group">
          <label>Tags (comma separated)</label>
          <input value={tags} onChange={(e) => setTags(e.target.value)} />
        </div>

        <div className="form-group">
          <label>
            Content{" "}
            <span className="form-hint">
              Use {"{{VAR_NAME}}"} for template variables
            </span>
          </label>
          <textarea
            value={content}
            onChange={(e) => setContent(e.target.value)}
            rows={12}
          />
        </div>

        <div className="modal-actions">
          <button className="btn" onClick={handleSave}>
            Save
          </button>
          <button className="btn btn-secondary" onClick={closeEditor}>
            Cancel
          </button>
        </div>
      </div>
    </div>
  );
}
