package mcpserver

import (
	"desktop/internal/hubclient"
	"desktop/internal/types"
)

// Storage wraps hubclient for MCP tool handlers.
type Storage struct {
	client      *hubclient.HubClient
	defaultRoom string
}

// NewStorage creates a new Storage backed by a hub client.
func NewStorage(client *hubclient.HubClient, defaultRoom string) *Storage {
	return &Storage{
		client:      client,
		defaultRoom: defaultRoom,
	}
}

// resolveRoom returns the room name, using defaultRoom if empty.
func (s *Storage) resolveRoom(room string) string {
	if room == "" {
		return s.defaultRoom
	}
	return room
}

// JoinRoom joins a room via the hub.
func (s *Storage) JoinRoom(room, agentName, role string) (*types.Response, error) {
	return s.client.JoinRoom(s.resolveRoom(room), agentName, role)
}

// SendMessage sends a message via the hub.
func (s *Storage) SendMessage(room, from, to, content string, expectsReply bool, priority string) (*types.Response, error) {
	return s.client.SendMessage(s.resolveRoom(room), from, to, content, expectsReply, priority)
}

// GetMessages reads messages via the hub.
func (s *Storage) GetMessages(room, agentName string, sinceID, limit int, unreadOnly bool) (*types.Response, error) {
	return s.client.GetMessages(s.resolveRoom(room), agentName, sinceID, limit, unreadOnly)
}

// GetAllMessages reads all messages via the hub.
func (s *Storage) GetAllMessages(room string, sinceID, limit int) (*types.Response, error) {
	return s.client.GetAllMessages(s.resolveRoom(room), sinceID, limit)
}

// ListAgents lists agents via the hub.
func (s *Storage) ListAgents(room, agentName string) (*types.Response, error) {
	return s.client.ListAgents(s.resolveRoom(room), agentName)
}

// LeaveRoom leaves a room via the hub.
func (s *Storage) LeaveRoom(room, agentName string) (*types.Response, error) {
	return s.client.LeaveRoom(s.resolveRoom(room), agentName)
}

// ClearRoom clears a room via the hub.
func (s *Storage) ClearRoom(room string) (*types.Response, error) {
	return s.client.ClearRoom(s.resolveRoom(room))
}

// GetLastMessageID gets the last message ID via the hub.
func (s *Storage) GetLastMessageID(room, agentName string) (*types.Response, error) {
	return s.client.GetLastMessageID(s.resolveRoom(room), agentName)
}

// ListRooms lists all rooms via the hub.
func (s *Storage) ListRooms() (*types.Response, error) {
	return s.client.ListRooms()
}
