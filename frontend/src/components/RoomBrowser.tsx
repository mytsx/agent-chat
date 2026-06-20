import { useEffect } from "react";
import { useRooms } from "../store/useRooms";
import { useTeams } from "../store/useTeams";
import { useMessages, useAgentsFor } from "../store/useMessages";
import MessageFeed from "./MessageFeed";
import { RoomSummary } from "../lib/types";

// truncateToMessages in the hub — rooms at/above this cap show only the most
// recent slice, not their lifetime total.
const MESSAGE_CAP = 300;

function relativeTime(iso: string): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (isNaN(t)) return "—";
  const sec = Math.floor((Date.now() - t) / 1000);
  if (sec < 60) return "az önce";
  const min = Math.floor(sec / 60);
  if (min < 60) return `${min} dk önce`;
  const hr = Math.floor(min / 60);
  if (hr < 24) return `${hr} saat önce`;
  const day = Math.floor(hr / 24);
  if (day < 30) return `${day} gün önce`;
  const mon = Math.floor(day / 30);
  if (mon < 12) return `${mon} ay önce`;
  return `${Math.floor(mon / 12)} yıl önce`;
}

function RoomRow({
  room,
  isActiveTeam,
  onClick,
}: {
  room: RoomSummary;
  isActiveTeam: boolean;
  onClick: () => void;
}) {
  const agentNames = Object.keys(room.agents || {});
  const isEmpty = room.message_count === 0 && agentNames.length === 0;
  const countLabel =
    room.message_count >= MESSAGE_CAP
      ? `son ${room.message_count} mesaj`
      : `${room.message_count} mesaj`;

  return (
    <button className="room-row" onClick={onClick}>
      <div className="room-row-top">
        <span className="room-row-name">
          {room.name}
          {room.is_default && <span className="room-tag">varsayılan</span>}
        </span>
        <span className="room-row-time">{relativeTime(room.last_activity)}</span>
      </div>
      <div className="room-row-meta">
        <span className="room-row-count">{countLabel}</span>
        <span
          className={`room-row-origin ${
            isActiveTeam ? "origin-team" : "origin-orphan"
          }`}
        >
          {isActiveTeam ? "takım" : "kayıtlı takım yok"}
        </span>
      </div>
      <div className="room-row-agents">
        {isEmpty ? (
          <span className="room-badge room-badge-empty">boş oda</span>
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
        ) : (
          <span className="room-agent-empty">
            agent kaydı yok (geçmiş odası)
          </span>
        )}
      </div>
    </button>
  );
}

function RoomDetail({
  room,
  isActiveTeam,
  onBack,
}: {
  room: string;
  isActiveTeam: boolean;
  onBack: () => void;
}) {
  const loadMessages = useMessages((s) => s.loadMessages);
  const loadAgents = useMessages((s) => s.loadAgents);
  const agents = useAgentsFor(room);

  useEffect(() => {
    loadMessages(room);
    loadAgents(room);
  }, [room, loadMessages, loadAgents]);

  const agentNames = Object.keys(agents);

  return (
    <div className="room-detail">
      <div className="room-detail-header">
        <button className="room-back" onClick={onBack}>
          ← Geri
        </button>
        <span className="room-detail-name">{room}</span>
        <span
          className={`room-live ${
            isActiveTeam ? "room-live-on" : "room-live-off"
          }`}
          title={
            isActiveTeam
              ? "Aktif takım odası — yeni mesajlar canlı düşer"
              : "Orphan oda — statik geçmiş, canlı akış yok"
          }
        >
          {isActiveTeam ? "● canlı" : "○ statik"}
        </span>
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
          <span className="room-agent-empty">
            agent kaydı yok (geçmiş odası)
          </span>
        )}
      </div>
      <MessageFeed chatDir={room} />
    </div>
  );
}

export default function RoomBrowser() {
  const rooms = useRooms((s) => s.rooms);
  const loading = useRooms((s) => s.loading);
  const selectedRoom = useRooms((s) => s.selectedRoom);
  const loadRooms = useRooms((s) => s.loadRooms);
  const selectRoom = useRooms((s) => s.selectRoom);
  const teams = useTeams((s) => s.teams);

  useEffect(() => {
    loadRooms();
  }, [loadRooms]);

  const teamNames = new Set(teams.map((t) => t.name));

  if (selectedRoom) {
    return (
      <RoomDetail
        room={selectedRoom}
        isActiveTeam={teamNames.has(selectedRoom)}
        onBack={() => selectRoom(null)}
      />
    );
  }

  return (
    <div className="room-browser">
      <div className="room-browser-header">
        <h3 className="sidebar-section-title">Rooms ({rooms.length})</h3>
        <button
          className="room-refresh"
          onClick={() => loadRooms()}
          title="Yenile"
        >
          ⟳
        </button>
      </div>
      {loading && rooms.length === 0 ? (
        <p className="sidebar-empty">Yükleniyor…</p>
      ) : rooms.length === 0 ? (
        <p className="sidebar-empty">Oda yok</p>
      ) : (
        <div className="room-list">
          {rooms.map((r) => (
            <RoomRow
              key={r.name}
              room={r}
              isActiveTeam={teamNames.has(r.name)}
              onClick={() => selectRoom(r.name)}
            />
          ))}
        </div>
      )}
    </div>
  );
}
