package eventlog

import "log/slog"

// Attribute keys. Anything OpenTelemetry already defines uses OTel's name
// verbatim, so this file can later be mapped onto OTLP without a rename pass;
// only things OTel has no name for get the agent_chat. prefix.
//
// OTel GenAI:  https://opentelemetry.io/docs/specs/semconv/registry/attributes/gen-ai/
// OTel MCP:    https://opentelemetry.io/docs/specs/semconv/registry/attributes/mcp/
// OTel events: https://opentelemetry.io/docs/specs/semconv/general/events/
const (
	// AttrEventName classifies the event. OTel requires it to uniquely identify
	// the record's structure and to carry NO dynamic values — anything that
	// varies per occurrence belongs in an attribute instead.
	AttrEventName = "event.name"

	// AttrConversationID is the room. OTel defines gen_ai.conversation.id as the
	// identifier of "a conversation (session, thread)", which is exactly what a
	// room is here.
	AttrConversationID = "gen_ai.conversation.id"
	AttrAgentName      = "gen_ai.agent.name"
	AttrToolName       = "gen_ai.tool.name"
	// AttrInputMessages carries message content and is opt-in per OTel, which
	// warns it may hold sensitive data. Gated by Options.CaptureContent and
	// truncated at maxContentBytes.
	AttrInputMessages = "gen_ai.input.messages"

	AttrMCPMethod          = "mcp.method.name"
	AttrMCPSessionID       = "mcp.session.id"
	AttrMCPProtocolVersion = "mcp.protocol.version"

	AttrRequestID        = "jsonrpc.request.id"
	AttrNetworkTransport = "network.transport"
	AttrServerPort       = "server.port"
	// AttrErrorType must stay low-cardinality (a class of error, never a
	// formatted message) so counting by it stays meaningful.
	AttrErrorType = "error.type"

	// AttrTraceID and AttrSpanID stay empty until #100 wires mcp-go v1.0.0's
	// tracing package. They are reserved here so events and spans line up the
	// day a tracer is installed, with no schema change.
	AttrTraceID = "trace_id"
	AttrSpanID  = "span_id"

	// Project-specific attributes: no OTel equivalent exists for these.
	AttrPID              = "agent_chat.pid"
	AttrClientType       = "agent_chat.client.type"
	AttrAgentRole        = "agent_chat.agent.role"
	AttrLeaveReason      = "agent_chat.leave.reason"
	AttrIdleSeconds      = "agent_chat.agent.idle_seconds"
	AttrRecipientName    = "agent_chat.recipient.name"
	AttrRecipientInRoom  = "agent_chat.recipient.in_room"
	AttrMessageID        = "agent_chat.message.id"
	AttrRerouteTarget    = "agent_chat.reroute.target"
	AttrReadSinceID      = "agent_chat.read.since_id"
	AttrReadReturned     = "agent_chat.read.returned"
	AttrReadMaxID        = "agent_chat.read.max_id"
	AttrContentTruncated = "agent_chat.content.truncated"
	AttrEventsDropped    = "agent_chat.events.dropped"
)

// Event names. Static and namespaced, per OTel's rule that an event name
// identifies the event class and never embeds a per-occurrence value.
const (
	EventHubStarted         = "agent_chat.hub.started"
	EventHubStopped         = "agent_chat.hub.stopped"
	EventClientConnected    = "agent_chat.client.connected"
	EventClientDisconnected = "agent_chat.client.disconnected"
	EventAgentJoined        = "agent_chat.agent.joined"
	EventAgentLeft          = "agent_chat.agent.left"
	EventAgentEvicted       = "agent_chat.agent.evicted"
	EventMessageSent        = "agent_chat.message.sent"
	EventMessageRerouted    = "agent_chat.message.rerouted"
	EventMessagesRead       = "agent_chat.messages.read"
	EventError              = "agent_chat.error"
)

// Values for AttrLeaveReason. Eviction is a separate event, not a reason,
// because it answers a different question than a disconnect does (#98).
const (
	LeaveReasonDisconnect = "disconnect"
	LeaveReasonExplicit   = "explicit"
)

// Values for AttrNetworkTransport, per OTel: "pipe" for stdio, "websocket" for
// a WebSocket transport.
const (
	TransportWebSocket = "websocket"
	TransportPipe      = "pipe"
)

// Attr is one key/value pair on an event. Aliased so callers (the hub) build
// events without importing log/slog themselves.
type Attr = slog.Attr

func String(key, value string) Attr { return slog.String(key, value) }
func Int(key string, value int) Attr {
	return slog.Int(key, value)
}
func Int64(key string, value int64) Attr     { return slog.Int64(key, value) }
func Uint64(key string, value uint64) Attr   { return slog.Uint64(key, value) }
func Bool(key string, value bool) Attr       { return slog.Bool(key, value) }
func Float64(key string, value float64) Attr { return slog.Float64(key, value) }
