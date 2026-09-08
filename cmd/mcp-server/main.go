package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"desktop/internal/hub"
	"desktop/internal/hubclient"
	"desktop/internal/mcpserver"
	"desktop/internal/validation"
)

func main() {
	// Mode selection. No flag = stdio MCP server, which is how the CLIs launch
	// this binary; the other modes are explicit.
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--hub":
			runHub()
			return
		case "--analyze":
			os.Exit(runAnalyze(os.Args[1:]))
		}
	}
	runMCP()
}

func runHub() {
	dataDir := os.Getenv("AGENT_CHAT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = home + "/.agent-chat"
	}

	defaultRoom := os.Getenv("AGENT_CHAT_ROOM")
	if defaultRoom == "" {
		defaultRoom = "default"
	}

	// Setup logger
	os.MkdirAll(dataDir, 0700)
	logFile, err := os.OpenFile(dataDir+"/mcp-server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logFile = os.Stderr
	}
	logger := log.New(logFile, "[HUB] ", log.LstdFlags|log.Lshortfile)

	h := hub.New(dataDir, defaultRoom, logger)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	shuttingDown := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		<-sigCh
		logger.Println("Received shutdown signal")
		close(shuttingDown)
		h.Shutdown()
		close(shutdownDone)
	}()

	logger.Println("Starting hub server...")
	if err := h.Run(0); err != nil {
		// Shutdown closes the listener, which makes Run return here. That is the
		// expected path on SIGTERM — do NOT exit, or we would race the signal
		// goroutine's durable shutdown work (archive drain + persistAll). Wait
		// for Shutdown to finish, then exit cleanly. Only a genuine startup/serve
		// error (no shutdown in progress) is fatal.
		select {
		case <-shuttingDown:
			<-shutdownDone
			os.Exit(0)
		default:
			logger.Printf("Hub server error: %v", err)
			os.Exit(1)
		}
	}
}

func runMCP() {
	dataDir := os.Getenv("AGENT_CHAT_DATA_DIR")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = home + "/.agent-chat"
	}

	defaultRoom := os.Getenv("AGENT_CHAT_ROOM")
	if defaultRoom == "" {
		defaultRoom = "default"
	}
	if err := validation.ValidateName(defaultRoom); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid AGENT_CHAT_ROOM: %v\n", err)
		os.Exit(1)
	}

	// Setup logger (file-based, since stdio is used for JSON-RPC)
	os.MkdirAll(dataDir, 0700)
	logFile, err := os.OpenFile(dataDir+"/mcp-server.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		logFile = os.Stderr
	}
	logger := log.New(logFile, "[MCP] ", log.LstdFlags|log.Lshortfile)

	// Discover hub address
	hubAddr, err := hubclient.DiscoverHubAddr(dataDir)
	if err != nil {
		logger.Printf("Hub discovery failed: %v", err)
		fmt.Fprintf(os.Stderr, "Hub not available: %v\n", err)
		os.Exit(1)
	}

	// Connect to hub
	client := hubclient.New(hubAddr, logger)
	if err := client.ConnectWithRetry(5); err != nil {
		logger.Printf("Hub connect failed: %v", err)
		fmt.Fprintf(os.Stderr, "Cannot connect to hub: %v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	logger.Printf("Connected to hub at %s", hubAddr)

	// Identify as an MCP client. Without this the hub only ever sees an
	// anonymous socket: connection telemetry cannot tell MCP from desktop, and
	// the connect event (emitted on identify) never fires for the CLI agents at
	// all. Non-fatal — a hub that rejects it must not stop the agent from
	// working, so the failure is logged and serving continues.
	if err := client.Identify("mcp", "", defaultRoom, ""); err != nil {
		logger.Printf("Identify failed (continuing unidentified): %v", err)
	}

	app := mcpserver.NewMCPServerApp(client, defaultRoom, logger)
	if err := app.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
