package main

import (
	"io"
	"log"
	"sync"
	"testing"

	"desktop/internal/hubclient"
)

// TestHubClientConcurrentAccess reproduces the data race from #56: monitorHub
// reassigns App.hubClient on hub-crash recovery (a write) while the Wails binding
// methods read it concurrently. With a plain *HubClient field this is flagged by
// `go test -race`; the field is an atomic.Pointer so the reconnect write (Store) and
// every binding read (Load) are synchronized.
//
// The non-nil clients are constructed unconnected (no Connect()), so the binding RPCs
// fail fast with "not connected to hub" — no network, no 15s timeout. The chosen
// readers (ListRooms/GetMessages/GetAgents/WatchChatDir) touch only a.hubClient, so
// they need no other App state. nil is included in the writer rotation to also stress
// the capture-to-local nil handling — safe only because each reader Loads the pointer
// once into a local (no nil-check-then-deref TOCTOU against a concurrent Store(nil)).
func TestHubClientConcurrentAccess(t *testing.T) {
	// The readers below log an expected "not connected" error via the global logger
	// for every non-nil-but-unconnected client they catch; silence it so this
	// concurrency test doesn't flood `go test` output (thousands of timing-dependent
	// lines). Restored on return; the package's tests run sequentially.
	oldOutput := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(oldOutput)

	app := &App{}
	logger := log.New(io.Discard, "", 0)
	clientA := hubclient.New("ws://127.0.0.1:0/ws", logger)
	clientB := hubclient.New("ws://127.0.0.1:0/ws", logger)

	const iterations = 2000
	var wg sync.WaitGroup

	// Writer: simulate connectToHub + monitorHub swapping (and clearing) the client.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range iterations {
			app.hubClient.Store(clientA)
			app.hubClient.Store(nil)
			app.hubClient.Store(clientB)
		}
	}()

	// Readers: the nil-safe Wails bindings that only touch a.hubClient.
	readers := []func(){
		func() { _, _ = app.ListRooms() },
		func() { _, _ = app.GetMessages("room") },
		func() { _, _ = app.GetAgents("room") },
		func() { _ = app.WatchChatDir("room") },
	}
	for _, r := range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				r()
			}
		}()
	}

	wg.Wait()
}
