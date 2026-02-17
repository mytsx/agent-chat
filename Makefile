.PHONY: build dev clean mcp-server

mcp-server:
	go build -o build/mcp-server-bin ./cmd/mcp-server

build: mcp-server
	wails build

dev: mcp-server
	wails dev

clean:
	rm -f build/mcp-server-bin
