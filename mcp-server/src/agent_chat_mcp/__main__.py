#!/usr/bin/env python3
"""
Entry point for Agent Chat Room MCP Server
Usage: python -m agent_chat_mcp
"""

from .server import mcp


def run():
    """Synchronous entry point"""
    mcp.run()


if __name__ == "__main__":
    run()
