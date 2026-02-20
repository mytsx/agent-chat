package main

import (
	"fmt"
	"os"

	"desktop/internal/mcpserver"
	"desktop/internal/validation"
)

func main() {
	chatDir := os.Getenv("AGENT_CHAT_DIR")
	if chatDir == "" {
		chatDir = "/tmp/agent-chat-room"
	}

	defaultRoom := os.Getenv("AGENT_CHAT_ROOM")
	if defaultRoom == "" {
		defaultRoom = "default"
	}
	if err := validation.ValidateName(defaultRoom); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid AGENT_CHAT_ROOM: %v\n", err)
		os.Exit(1)
	}

	app := mcpserver.NewMCPServerApp(chatDir, defaultRoom)
	if err := app.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
