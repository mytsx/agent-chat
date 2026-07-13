import { useEffect, useRef } from "react";
import { useMessagesFor } from "../store/useMessages";
import { type Message } from "../lib/types";

interface Props {
  chatDir: string;
  // Max messages to render (most recent). 0 = no limit (show full history).
  // Defaults to 50 for the sidebar preview; the room browser passes 0.
  maxMessages?: number;
}

export default function MessageFeed({ chatDir, maxMessages = 50 }: Props) {
  const messages = useMessagesFor(chatDir);
  const bottomRef = useRef<HTMLDivElement>(null);
  const visibleMessages = maxMessages > 0 ? messages.slice(-maxMessages) : messages;

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages.length]);

  if (messages.length === 0) {
    return (
      <div className="message-feed" aria-label="Messages">
        <h3 className="sidebar-section-title">Messages</h3>
        <p className="sidebar-empty" aria-live="polite">No messages yet</p>
      </div>
    );
  }

  return (
    <div className="message-feed" aria-label="Messages">
      <h3 className="sidebar-section-title">
        Messages ({messages.length})
      </h3>
      <div
        className="message-list"
        role="list"
        aria-live="polite"
        aria-relevant="additions text"
        aria-label={maxMessages > 0 ? `Latest ${visibleMessages.length} messages` : "All messages"}
      >
        {visibleMessages.map((msg) => {
          const time = msg.timestamp
            ? // Truncate Go's microsecond precision to ms; Safari/WKWebView
              // return Invalid Date for >3 fractional digits.
              new Date(msg.timestamp.replace(/(\.\d{3})\d+/, "$1")).toLocaleTimeString([], {
                hour: "2-digit",
                minute: "2-digit",
                second: "2-digit",
              })
            : "";

          if (msg.type === "system") {
            return (
              <div
                key={msg.id}
                className="msg msg-system"
                role="listitem"
                aria-label={messageAccessibilityLabel(msg, time)}
              >
                <span className="msg-time">{time}</span>
                <span className="msg-content">{msg.content}</span>
              </div>
            );
          }

          return (
            <div
              key={msg.id}
              className={`msg ${msg.priority === "urgent" ? "msg-urgent" : ""}`}
              role="listitem"
              aria-label={messageAccessibilityLabel(msg, time)}
            >
              <div className="msg-header">
                <span className="msg-from">{msg.from}</span>
                <span className="msg-arrow">
                  {msg.to === "all" ? "=> ALL" : `=> ${msg.to}`}
                  {msg.original_to && msg.original_to !== msg.to ? ` (intended: ${msg.original_to})` : null}
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

function messageAccessibilityLabel(msg: Message, time: string): string {
  const timePrefix = time ? `${time}, ` : "";
  if (msg.type === "system") {
    return `${timePrefix}system message`;
  }

  const recipient = msg.to === "all" ? "all" : msg.to;
  const route = msg.original_to && msg.original_to !== msg.to
    ? `, originally intended for ${msg.original_to}`
    : "";
  const priority = msg.priority === "urgent" ? "urgent, " : "";
  return `${timePrefix}${priority}message from ${msg.from} to ${recipient}${route}`;
}
