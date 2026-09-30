// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/mcpserver"
	"github.com/limelit-co/open/internal/provider"
)

// connectInfo is what the startup message needs to say how to reach this
// instance.
type connectInfo struct {
	version string
	// addr is the address the listener actually bound.
	addr net.Addr
	// exe and dataDir are absolute, so the commands printed work from any
	// directory: Claude starts `limelit mcp` from its own, not this one.
	exe, dataDir string
	// state is mcpserver.Status's data_state, so this message and a
	// connected assistant's first answer describe the same instance.
	state string
	// httpToken is true when an MCP bearer token is in force.
	httpToken bool
	// container is this container's ID when running in Docker, else "".
	container string
}

// writeConnect prints every way into a running instance: the dashboard, MCP
// over stdio for Claude Code and Claude Desktop, and MCP over HTTP. The
// commands carry this machine's real paths, so each one can be pasted as is.
func writeConnect(w io.Writer, c connectInfo) {
	base := baseURL(c.addr)

	var b strings.Builder
	fmt.Fprintf(&b, "\nLimelit Open %s is running. Connect with any of these:\n\n", c.version)

	b.WriteString("  Browser\n")
	fmt.Fprintf(&b, "    %s\n", base)
	if c.state == mcpserver.StateNotSetUp {
		b.WriteString("    The first visit opens the setup wizard.\n")
	}

	// Stdio: Claude launches the binary itself. In a container that means
	// through docker exec, where the image already sets the data directory.
	var shell string
	var desktop stdioServer
	if c.container != "" {
		shell = "claude mcp add limelit -s user -- docker exec -i " + c.container + " limelit mcp"
		desktop = stdioServer{Command: "docker", Args: []string{"exec", "-i", c.container, "limelit", "mcp"}}
	} else {
		shell = "claude mcp add limelit -s user -e " + shellQuote(config.DataDirEnv+"="+c.dataDir) + " -- " + shellQuote(c.exe) + " mcp"
		desktop = stdioServer{Command: c.exe, Args: []string{"mcp"}, Env: map[string]string{config.DataDirEnv: c.dataDir}}
	}
	b.WriteString("\n  Claude Code\n")
	fmt.Fprintf(&b, "    %s\n", shell)

	b.WriteString("\n  Claude Desktop: put this in claude_desktop_config.json\n")
	cfg, _ := json.Marshal(map[string]map[string]stdioServer{"mcpServers": {"limelit": desktop}})
	fmt.Fprintf(&b, "    %s\n", cfg)

	b.WriteString("\n  Any MCP client, over HTTP\n")
	fmt.Fprintf(&b, "    %s/mcp\n", base)
	if c.httpToken {
		b.WriteString("    Send your token as Authorization: Bearer <token>.\n")
	} else {
		fmt.Fprintf(&b, "    Needs a token first: generate one in Settings, or set %s.\n", mcpserver.TokenEnv)
	}

	b.WriteString("\n  Next\n")
	for i, step := range nextSteps(c.state, base, loginCommand(c)) {
		fmt.Fprintf(&b, "    %d. %s\n", i+1, step)
	}
	b.WriteString("\n")

	io.WriteString(w, b.String())
}

// startPhrase is what a user types in any assistant. It names Limelit, which
// is enough for a client to find this server among many, and the server
// instructions and get_active_property's description take it from there.
const startPhrase = `"Get started with Limelit"`

// loginCommand is `limelit login` as it has to be typed in a second terminal
// to save into this instance: with this binary and this data directory, or
// through docker exec, where the image already sets the data directory.
func loginCommand(c connectInfo) string {
	if c.container != "" {
		return "docker exec -it " + c.container + " limelit login"
	}
	return shellQuote(config.DataDirEnv+"="+c.dataDir) + " " + shellQuote(c.exe) + " login"
}

// nextSteps is the short path from this state to a first answer, ending with
// what to type in the assistant, so a user never has to learn a tool name.
func nextSteps(state, dashboard, login string) []string {
	ask := "Then ask your assistant: " + startPhrase + "\n       or: \"" + mcpserver.TryAsking[0] + "\""
	switch state {
	case mcpserver.StateNotSetUp, mcpserver.StateSetupIncomplete:
		return []string{
			"Finish setup at " + dashboard + ": your brand, competitors, prompts and one key.\n" +
				"       No provider key? Get a free Limelit Cloud key here:\n" +
				"         " + provider.LimelitKeyPage + "\n" +
				"       Sign in with Google, copy the key, and paste it in setup. Or, in a second terminal:\n" +
				"         " + login,
			"Press Run in the dashboard to fetch the first answers.",
			ask,
		}
	case mcpserver.StateNeverRun:
		return []string{"Press Run at " + dashboard + " to fetch the first answers.", ask}
	case mcpserver.StateStale:
		return []string{"The last 30 days hold no answers: press Run at " + dashboard + " for fresh ones.", ask}
	default:
		return []string{"Ask your assistant: " + startPhrase + "\n       or: \"" + mcpserver.TryAsking[0] + "\""}
	}
}

// stdioServer is one entry under mcpServers in Claude Desktop's config.
type stdioServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}

// baseURL is the address a browser on this machine uses: a listener on every
// interface is reached at localhost.
func baseURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// shellQuote quotes s for a POSIX shell when it needs it, so a path with a
// space still pastes as one argument.
func shellQuote(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// containerID is this container's ID when running under Docker or Podman,
// which is also its hostname unless the run overrode it.
func containerID() string {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			if h, err := os.Hostname(); err == nil {
				return h
			}
		}
	}
	return ""
}

// executablePath is this binary's absolute path with symlinks resolved, or
// "limelit" when the system will not say.
func executablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "limelit"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// absPath is p made absolute, or p as given when it cannot be.
func absPath(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
