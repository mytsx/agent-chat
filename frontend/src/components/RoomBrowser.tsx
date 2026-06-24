import { useEffect, useState } from "react";
import { useRooms } from "../store/useRooms";
import { useTeams } from "../store/useTeams";
import { useMessages, useAgentsFor } from "../store/useMessages";
import MessageFeed from "./MessageFeed";
import RoomSummaryModal from "./RoomSummaryModal";
import ImportRoomModal from "./ImportRoomModal";
import { RoomSummary } from "../lib/types";

// truncateToMessages in the hub — rooms at/above this cap show only the most
// recent slice, not their lifetime total.
const MESSAGE_CAP = 300;

function relativeTime(iso: string): string {
  if (!iso) return "—";
  // Go emits microsecond precision (e.g. ".000000"); Safari/WKWebView return an
  // Invalid Date for >3 fractional digits, so truncate to milliseconds first.
  const t = new Date(iso.replace(/(\.\d{3})\d+/, "$1")).getTime();
  if (isNaN(t)) return "—";
  const sec = Math.max(0, Math.floor((Date.now() - t) / 1000));
  if (sec < 60) return "just now";
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day}d ago`;
  const mon = Math.floor(day / 30);
  if (mon < 12) return `${mon}mo ago`;
  return `${Math.floor(mon / 12)}y ago`;
}

function RoomRow({
  room,
  isActiveTeam,
  onClick,
  onDelete,
}: {
  room: RoomSummary;
  isActiveTeam: boolean;
  onClick: () => void;
  onDelete: () => void;
}) {
  const agentNames = Object.keys(room.agents || {});
  const isEmpty = room.message_count === 0 && agentNames.length === 0;
  const countLabel =
    room.message_count >= MESSAGE_CAP
      ? `last ${room.message_count} messages`
      : `${room.message_count} messages`;

  return (
    <div
      className="room-row"
      role="button"
      tabIndex={0}
      onClick={onClick}
      onKeyDown={(e) => {
        // Only act when the row itself is focused — keydown bubbles, so without this
        // the nested delete button's Enter/Space would also select the room.
        if (
          e.target === e.currentTarget &&
          (e.key === "Enter" || e.key === " ")
        ) {
          e.preventDefault();
          onClick();
        }
      }}
    >
      <div className="room-row-top">
        <span className="room-row-name">
          {room.name}
          {room.is_default && <span className="room-tag">default</span>}
        </span>
        <span className="room-row-time">
          {relativeTime(room.last_activity)}
        </span>
        {!isActiveTeam && !room.is_default && (
          <button
            className="room-delete"
            title="Bu orphan odayı sil"
            onClick={(e) => {
              e.stopPropagation();
              onDelete();
            }}
          >
            🗑
          </button>
        )}
      </div>
      <div className="room-row-meta">
        <span className="room-row-count">{countLabel}</span>
        <span
          className={`room-row-origin ${
            isActiveTeam ? "origin-team" : "origin-orphan"
          }`}
        >
          {isActiveTeam ? "team" : "no team"}
        </span>
      </div>
      <div className="room-row-agents">
        {isEmpty ? (
          <span className="room-badge room-badge-empty">empty room</span>
        ) : agentNames.length > 0 ? (
          agentNames.map((n) => (
            <span
              key={n}
              className="room-agent-badge"
              title={room.agents[n]?.role || ""}
            >
              {n}
            </span>
          ))
        ) : room.historical_agents?.length > 0 ? (
          <span className="room-historical">
            <span className="room-historical-label">geçmişte bulunmuş:</span>
            {room.historical_agents.map((n) => (
              <span
                key={n}
                className="room-agent-badge room-agent-badge-historical"
              >
                {n}
              </span>
            ))}
          </span>
        ) : (
          <span className="room-agent-empty">no agents (archived room)</span>
        )}
      </div>
    </div>
  );
}

function RoomDetail({
  room,
  summary,
  isActiveTeam,
  onBack,
  onImported,
}: {
  room: string;
  summary: RoomSummary | undefined;
  isActiveTeam: boolean;
  onBack: () => void;
  onImported: () => void;
}) {
  const loadMessages = useMessages((s) => s.loadMessages);
  const loadAgents = useMessages((s) => s.loadAgents);
  const agents = useAgentsFor(room);
  const [showSummary, setShowSummary] = useState(false);
  const [showImport, setShowImport] = useState(false);

  useEffect(() => {
    loadMessages(room);
    loadAgents(room);
  }, [room, loadMessages, loadAgents]);

  // useAgentsFor already returns a stable {} when absent, so this never throws;
  // the `|| {}` is defense-in-depth, matching RoomRow's guard.
  const agentNames = Object.keys(agents || {});

  return (
    <div className="room-detail">
      <div className="room-detail-header">
        <button className="room-back" onClick={onBack}>
          ← Back
        </button>
        <span className="room-detail-name">{room}</span>
        <span
          className={`room-live ${
            isActiveTeam ? "room-live-on" : "room-live-off"
          }`}
          title={
            isActiveTeam
              ? "Active team room — new messages stream live"
              : "Orphaned room — static history, no live stream"
          }
        >
          {isActiveTeam ? "● live" : "○ static"}
        </span>
        <button
          className="room-back"
          title="Bu session'ın özetini üret / düzenle"
          onClick={() => setShowSummary(true)}
        >
          📝 Özet
        </button>
        {!isActiveTeam && summary && (
          <button
            className="room-back"
            title="Bu orphan odayı yeni takım olarak içe aktar"
            onClick={() => setShowImport(true)}
          >
            ⬇️ Takıma Aktar
          </button>
        )}
      </div>
      <div className="room-detail-agents">
        {agentNames.length > 0 ? (
          agentNames.map((n) => (
            <span
              key={n}
              className="room-agent-badge"
              title={agents[n]?.role || ""}
            >
              {n}
            </span>
          ))
        ) : (
          <span className="room-agent-empty">no agents (archived room)</span>
        )}
      </div>
      {/* maxMessages=0 → show full retained history, not the sidebar's 50-msg preview */}
      <MessageFeed chatDir={room} maxMessages={0} />

      {showSummary && (
        <RoomSummaryModal room={room} onClose={() => setShowSummary(false)} />
      )}

      {showImport && summary && (
        <ImportRoomModal
          room={summary}
          onClose={() => setShowImport(false)}
          onImported={() => {
            setShowImport(false);
            onImported();
          }}
        />
      )}
    </div>
  );
}

export default function RoomBrowser() {
  const rooms = useRooms((s) => s.rooms);
  const loading = useRooms((s) => s.loading);
  const error = useRooms((s) => s.error);
  const selectedRoom = useRooms((s) => s.selectedRoom);
  const loadRooms = useRooms((s) => s.loadRooms);
  const selectRoom = useRooms((s) => s.selectRoom);
  const deleteRoom = useRooms((s) => s.deleteRoom);
  const teams = useTeams((s) => s.teams);
  const [pendingDeleteRoom, setPendingDeleteRoom] = useState<string | null>(
    null,
  );
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deletingRoom, setDeletingRoom] = useState<string | null>(null);

  useEffect(() => {
    loadRooms();
  }, [loadRooms]);

  const teamNames = new Set((teams || []).map((t) => t.name));

  async function confirmDeleteRoom() {
    if (!pendingDeleteRoom) return;
    setDeletingRoom(pendingDeleteRoom);
    setDeleteError(null);
    try {
      await deleteRoom(pendingDeleteRoom);
      setPendingDeleteRoom(null);
    } catch (e) {
      setDeleteError(e instanceof Error ? e.message : String(e));
    } finally {
      setDeletingRoom(null);
    }
  }

  if (selectedRoom) {
    return (
      <RoomDetail
        room={selectedRoom}
        summary={rooms.find((r) => r.name === selectedRoom)}
        isActiveTeam={teamNames.has(selectedRoom)}
        onBack={() => selectRoom(null)}
        onImported={() => {
          selectRoom(null);
          loadRooms();
        }}
      />
    );
  }

  return (
    <div className="room-browser">
      <div className="room-browser-header">
        <h3 className="sidebar-section-title">Rooms ({rooms.length})</h3>
        <button
          className={`room-refresh${loading ? " loading" : ""}`}
          onClick={() => loadRooms()}
          disabled={loading}
          title="Refresh"
        >
          ⟳
        </button>
      </div>
      {error ? (
        <p className="sidebar-empty room-error">
          Failed to load rooms: {error}
        </p>
      ) : loading && rooms.length === 0 ? (
        <p className="sidebar-empty">Loading…</p>
      ) : rooms.length === 0 ? (
        <p className="sidebar-empty">No rooms</p>
      ) : (
        <div className="room-list">
          {rooms.map((r) => (
            <RoomRow
              key={r.name}
              room={r}
              isActiveTeam={teamNames.has(r.name)}
              onClick={() => selectRoom(r.name)}
              onDelete={() => {
                setDeleteError(null);
                setPendingDeleteRoom(r.name);
              }}
            />
          ))}
        </div>
      )}
      {pendingDeleteRoom && (
        <div
          className="modal-overlay"
          onClick={() => {
            if (!deletingRoom) {
              setPendingDeleteRoom(null);
              setDeleteError(null);
            }
          }}
        >
          <div
            className="modal room-delete-modal"
            onClick={(e) => e.stopPropagation()}
          >
            <h3>Delete room</h3>
            <p className="room-delete-copy">
              <strong>{pendingDeleteRoom}</strong> odası silinsin mi? Mesaj
              geçmişi kaldırılır, arşiv dosyaları korunur.
            </p>
            {deleteError && (
              <p className="form-error">Silme başarısız: {deleteError}</p>
            )}
            <div className="modal-actions">
              <button
                className="room-back"
                onClick={() => {
                  setPendingDeleteRoom(null);
                  setDeleteError(null);
                }}
                disabled={!!deletingRoom}
              >
                Vazgeç
              </button>
              <button
                className="room-delete-confirm"
                onClick={() => {
                  void confirmDeleteRoom();
                }}
                disabled={!!deletingRoom}
              >
                {deletingRoom ? "Siliniyor..." : "Sil"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
