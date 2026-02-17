package main

import (
	"fmt"
	"os"

	"desktop/internal/mcpserver"
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

	app := mcpserver.NewMCPServerApp(chatDir, defaultRoom)
	if err := app.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
