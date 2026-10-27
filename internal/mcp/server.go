// Package mcp exposes FleetDeck's read-mostly state to coding agents over the
// Model Context Protocol. It speaks MCP over stdio and reaches the hub through
// its HTTP REST API, so it never touches Postgres directly.
package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation metadata advertised to MCP clients.
const (
	ServerName    = "fleetdeck-mcp"
	ServerVersion = "0.1.0"
)

// NewServer builds an MCP server backed by hub and registers every FleetDeck
// tool on it. The returned server is not connected to a transport; call
// Server.Run (or Server.Connect) to serve it.
func NewServer(hub *HubClient) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    ServerName,
		Version: ServerVersion,
	}, &mcp.ServerOptions{
		Instructions: "Inspect, steer, and resume FleetDeck agent sessions. All tools are read-only except fleetdeck_session_send_message.",
	})

	registerToolset(s, hub)
	return s
}

// registerToolset wires every tool handler. Kept in one place so cmd and tests
// share the exact same surface.
func registerToolset(s *mcp.Server, hub *HubClient) {
	registerSessionStatus(s, hub)
	registerTaskList(s, hub)
	registerSubtaskList(s, hub)
	registerMessageTail(s, hub)
	registerEventTail(s, hub)
	registerSendMessage(s, hub)
}
