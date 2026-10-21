package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"fleetdeck/internal/node"
	"fleetdeck/internal/protocol"
)

// workspaceCommand manages a node's workspaces from its own machine:
//
//	fleetdeck-node workspace add --path /srv/code/app [--name app]
//	fleetdeck-node workspace list
//
// The hub is the only database writer, so these commands are thin REST
// clients: they authenticate as a *user* (the same FLEETDECK_HUB_TOKEN the MCP
// server uses, or --email/--password to log in) and resolve the node from the
// local node config. Node credentials never authorize REST calls by design.
func runWorkspace(args []string) error {
	if len(args) == 0 {
		return errors.New("workspace: missing subcommand (add or list)")
	}
	switch args[0] {
	case "add":
		return runWorkspaceAdd(args[1:])
	case "list":
		return runWorkspaceList(args[1:])
	default:
		return fmt.Errorf("workspace: unknown subcommand %q (want add or list)", args[0])
	}
}

// workspaceFlags are the shared connection flags.
type workspaceFlags struct {
	configPath string
	nodeRef    string
	hubURL     string
	token      string
	email      string
	password   string
}

func (f *workspaceFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.configPath, "config", "", "node config file (default: the platform config file)")
	fs.StringVar(&f.nodeRef, "node", "", "node id or name (default: the name in the node config)")
	fs.StringVar(&f.hubURL, "hub-url", "", "hub REST base URL (default: $FLEETDECK_HUB_URL, then the node config's hub_url)")
	fs.StringVar(&f.token, "token", "", "hub bearer token (default: $FLEETDECK_HUB_TOKEN)")
	fs.StringVar(&f.email, "email", "", "hub account email; used only when no token is available (default: $FLEETDECK_HUB_EMAIL)")
	fs.StringVar(&f.password, "password", "", "hub account password for --email (default: $FLEETDECK_HUB_PASSWORD; prefer the env var)")
}

func runWorkspaceAdd(args []string) error {
	fs := flag.NewFlagSet("workspace add", flag.ContinueOnError)
	var flags workspaceFlags
	flags.bind(fs)
	name := fs.String("name", "", "workspace display name (default: the path's base name)")
	pathFlag := fs.String("path", "", "absolute directory on the node (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pathFlag == "" {
		return errors.New("workspace add: --path is required")
	}
	dir := strings.TrimSpace(*pathFlag)
	if !isAbsolutePath(dir) {
		return fmt.Errorf("workspace add: path %q must be absolute", dir)
	}
	displayName := strings.TrimSpace(*name)
	if displayName == "" {
		displayName = path.Base(strings.ReplaceAll(dir, "\\", "/"))
	}
	if displayName == "" || displayName == "." || displayName == "/" {
		return errors.New("workspace add: cannot derive a name from the path; pass --name")
	}

	client, err := newHubClient(flags)
	if err != nil {
		return err
	}
	nodeID, err := client.resolveNode(flags.nodeRef)
	if err != nil {
		return err
	}
	var created protocol.Workspace
	if err := client.do(http.MethodPost, "/api/workspaces", map[string]string{
		"node_id": nodeID,
		"name":    displayName,
		"path":    dir,
	}, &created); err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	fmt.Printf("Created workspace %q (%s) at %s on node %s\n", created.Name, created.ID, created.Path, nodeID)
	return nil
}

func runWorkspaceList(args []string) error {
	fs := flag.NewFlagSet("workspace list", flag.ContinueOnError)
	var flags workspaceFlags
	flags.bind(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := newHubClient(flags)
	if err != nil {
		return err
	}
	nodeID, err := client.resolveNode(flags.nodeRef)
	if err != nil {
		return err
	}
	var workspaces []protocol.Workspace
	if err := client.do(http.MethodGet, "/api/workspaces?node_id="+url.QueryEscape(nodeID), nil, &workspaces); err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	if len(workspaces) == 0 {
		fmt.Printf("No workspaces on node %s\n", nodeID)
		return nil
	}
	for _, ws := range workspaces {
		fmt.Printf("%s\t%s\t%s\n", ws.ID, ws.Name, ws.Path)
	}
	return nil
}

// hubClient is the minimal REST client these commands need.
type hubClient struct {
	base        string
	token       string
	defaultNode string
	http        *http.Client
}

func newHubClient(flags workspaceFlags) (*hubClient, error) {
	cfgPath, _, err := node.ConfigPath(flags.configPath)
	if err != nil {
		return nil, err
	}
	cfg, err := node.LoadConfig(cfgPath)
	if err != nil {
		return nil, err
	}
	base, err := resolveHubBase(flags.hubURL, cfg.HubURL)
	if err != nil {
		return nil, err
	}
	c := &hubClient{
		base:        base,
		defaultNode: cfg.Name,
		http:        &http.Client{Timeout: 15 * time.Second},
	}

	c.token = firstNonEmpty(flags.token, os.Getenv("FLEETDECK_HUB_TOKEN"))
	if c.token == "" {
		email := firstNonEmpty(flags.email, os.Getenv("FLEETDECK_HUB_EMAIL"))
		password := firstNonEmpty(flags.password, os.Getenv("FLEETDECK_HUB_PASSWORD"))
		if email == "" || password == "" {
			return nil, errors.New("workspace: no hub credentials: pass --token (or set FLEETDECK_HUB_TOKEN), or --email/--password (or FLEETDECK_HUB_EMAIL/FLEETDECK_HUB_PASSWORD)")
		}
		var session struct {
			Token string `json:"token"`
		}
		if err := c.do(http.MethodPost, "/api/auth/login", map[string]string{
			"email":    email,
			"password": password,
		}, &session); err != nil {
			return nil, fmt.Errorf("hub login: %w", err)
		}
		c.token = session.Token
	}
	return c, nil
}

// resolveNode maps the configured node name (or an explicit id/name) to a node
// id visible to the signed-in user. An admin sees every node; a bound user
// only their nodes — an invisible node therefore reports "not found".
func (c *hubClient) resolveNode(ref string) (string, error) {
	if ref == "" {
		ref = c.defaultNode
	}
	if ref == "" {
		return "", errors.New("workspace: cannot determine the node; pass --node <id|name> or set name in the node config")
	}
	var nodes []protocol.Node
	if err := c.do(http.MethodGet, "/api/nodes", nil, &nodes); err != nil {
		return "", fmt.Errorf("list nodes: %w", err)
	}
	for _, n := range nodes {
		if n.ID == ref || n.Name == ref {
			return n.ID, nil
		}
	}
	return "", fmt.Errorf("workspace: node %q not found (or not visible to this user)", ref)
}

// do issues one JSON request and decodes a 2xx response into out (when non-nil).
func (c *hubClient) do(method, apiPath string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+apiPath, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Error != "" {
			return fmt.Errorf("hub returned %d: %s", resp.StatusCode, apiErr.Error)
		}
		return fmt.Errorf("hub returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode hub response: %w", err)
	}
	return nil
}

// resolveHubBase finds the hub REST base URL: the flag, then FLEETDECK_HUB_URL,
// then the node config's WS endpoint (ws://host/ws/node -> http://host), then
// FLEETDECK_HUB_ADDR.
func resolveHubBase(flagURL, configWSURL string) (string, error) {
	if flagURL != "" {
		return normalizeBase(flagURL)
	}
	if envURL := os.Getenv("FLEETDECK_HUB_URL"); envURL != "" {
		return normalizeBase(envURL)
	}
	if configWSURL != "" {
		u, err := url.Parse(configWSURL)
		if err != nil {
			return "", fmt.Errorf("workspace: parse configured hub_url %q: %w", configWSURL, err)
		}
		switch u.Scheme {
		case "ws":
			u.Scheme = "http"
		case "wss":
			u.Scheme = "https"
		case "http", "https":
		default:
			return "", fmt.Errorf("workspace: unsupported hub_url scheme %q", u.Scheme)
		}
		u.Path, u.RawQuery, u.Fragment = "", "", ""
		return strings.TrimRight(u.String(), "/"), nil
	}
	if addr := os.Getenv("FLEETDECK_HUB_ADDR"); addr != "" {
		return normalizeBase("http://" + addr)
	}
	return "", errors.New("workspace: no hub endpoint: pass --hub-url, set FLEETDECK_HUB_URL, or set hub_url in the node config")
}

func normalizeBase(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("workspace: parse hub URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("workspace: hub URL %q must use http or https", raw)
	}
	if u.Host == "" {
		return "", fmt.Errorf("workspace: hub URL %q has no host", raw)
	}
	base := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	if base == "" {
		return "", fmt.Errorf("workspace: hub URL %q is empty", raw)
	}
	return base, nil
}

// isAbsolutePath applies the workspace-path rule shared with the web form:
// a leading slash or a Windows drive prefix.
func isAbsolutePath(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') {
		c := p[0]
		return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
