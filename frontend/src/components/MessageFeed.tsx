import { useEffect, useRef } from "react";
import { useMessagesFor } from "../store/useMessages";

interface Props {
  chatDir: string;
}

export default function MessageFeed({ chatDir }: Props) {
  const messages = useMessagesFor(chatDir);
  const bottomRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages.length]);

  if (messages.length === 0) {
    return (
      <div className="message-feed">
        <h3 className="sidebar-section-title">Messages</h3>
        <p className="sidebar-empty">No messages yet</p>
      </div>
    );
  }

  return (
    <div className="message-feed">
      <h3 className="sidebar-section-title">
        Messages ({messages.length})
      </h3>
      <div className="message-list">
        {messages.slice(-50).map((msg) => {
          const time = msg.timestamp
            ? new Date(msg.timestamp).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
              })
            : "";

          if (msg.type === "system") {
            return (
              <div key={msg.id} className="msg msg-system">
                <span className="msg-time">{time}</span>
                <span className="msg-content">{msg.content}</span>
              </div>
            );
          }

          return (
            <div
              key={msg.id}
              className={`msg ${msg.priority === "urgent" ? "msg-urgent" : ""}`}
            >
              <div className="msg-header">
                <span className="msg-from">{msg.from}</span>
                <span className="msg-arrow">
                  {msg.to === "all" ? "=> ALL" : `=> ${msg.to}`}
                </span>
                <span className="msg-time">{time}</span>
              </div>
              <div className="msg-content">{msg.content}</div>
            </div>
          );
        })}
        <div ref={bottomRef} />
      </div>
    </div>
  );
}
