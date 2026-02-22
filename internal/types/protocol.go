package types

import "encoding/json"

// Request is a client-to-hub message envelope.
type Request struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Room string          `json:"room"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Response is a hub-to-client reply for a specific request.
type Response struct {
	ID          string          `json:"id"`
	RequestType string          `json:"request_type"`
	Success     bool            `json:"success"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
}

// Event is a hub-to-subscriber broadcast.
type Event struct {
	Type  string          `json:"type"`
	Event string          `json:"event"`
	Room  string          `json:"room"`
	Data  json.RawMessage `json:"data,omitempty"`
}
