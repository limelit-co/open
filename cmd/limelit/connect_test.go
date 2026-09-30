// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package main

import (
	"encoding/json"
	"net"
	"os/exec"
	"strings"
	"testing"

	"github.com/limelit-co/open/internal/mcpserver"
)

func tcpAddr(t *testing.T, s string) net.Addr {
	t.Helper()
	a, err := net.ResolveTCPAddr("tcp", s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func connectText(c connectInfo) string {
	var b strings.Builder
	writeConnect(&b, c)
	return b.String()
}

// lineAfter is the first line after the heading that starts with prefix.
func lineAfter(t *testing.T, text, heading string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, heading)
	if !ok {
		t.Fatalf("no %q in:\n%s", heading, text)
	}
	for _, l := range strings.Split(rest, "\n")[1:] {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	t.Fatalf("nothing under %q", heading)
	return ""
}

// TestConnectNamesEveryWayIn. A fresh start has to say where the dashboard
// is and how each kind of client connects, with this machine's paths filled
// in, because Claude starts `limelit mcp` from its own directory.
func TestConnectNamesEveryWayIn(t *testing.T) {
	out := connectText(connectInfo{
		version: "v0.1.0", addr: tcpAddr(t, "[::]:1515"),
		exe: "/home/ana/limelit/limelit", dataDir: "/home/ana/limelit/data", state: mcpserver.StateNotSetUp,
	})
	for _, want := range []string{
		"http://localhost:1515\n",
		"setup wizard",
		"claude mcp add limelit -s user -e LIMELIT_DATA_DIR=/home/ana/limelit/data -- /home/ana/limelit/limelit mcp",
		"http://localhost:1515/mcp",
		"generate one in Settings",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	var cfg struct {
		MCPServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(lineAfter(t, out, "Claude Desktop")), &cfg); err != nil {
		t.Fatalf("the Claude Desktop config is not JSON: %v", err)
	}
	s := cfg.MCPServers["limelit"]
	if s.Command != "/home/ana/limelit/limelit" || strings.Join(s.Args, " ") != "mcp" || s.Env["LIMELIT_DATA_DIR"] != "/home/ana/limelit/data" {
		t.Errorf("Claude Desktop entry = %+v", s)
	}

	if strings.ContainsAny(out, "—–") {
		t.Error("the startup message has an em or en dash")
	}
}

// TestConnectSaysWhatIsAlreadyTrue. A finished setup does not send anyone to
// the wizard, and a token in force is not asked for again.
func TestConnectSaysWhatIsAlreadyTrue(t *testing.T) {
	out := connectText(connectInfo{
		version: "v0.1.0", addr: tcpAddr(t, "127.0.0.1:1515"),
		exe: "/x/limelit", dataDir: "/x/data", state: mcpserver.StateReady, httpToken: true,
	})
	if strings.Contains(out, "setup wizard") {
		t.Error("a set-up instance still points at the wizard")
	}
	if strings.Contains(out, "generate one in Settings") || !strings.Contains(out, "Authorization: Bearer <token>") {
		t.Errorf("token in force, but:\n%s", out)
	}
}

// TestConnectPathsWithSpacesStillPaste. A path with a space has to reach
// the shell as one argument, and the JSON has to keep it whole.
func TestConnectPathsWithSpacesStillPaste(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	exe, data := "/Users/ana/My Tools/limelit", "/Users/ana/My Tools/it's data"
	out := connectText(connectInfo{version: "v0.1.0", addr: tcpAddr(t, "[::]:1515"), exe: exe, dataDir: data})

	cmd := lineAfter(t, out, "Claude Code")
	args := strings.TrimPrefix(cmd, "claude ")
	got, err := exec.Command("sh", "-c", `for a in `+args+`; do printf '%s\n' "$a"; done`).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{"mcp", "add", "limelit", "-s", "user", "-e", "LIMELIT_DATA_DIR=" + data, "--", exe, "mcp"}, "\n") + "\n"
	if string(got) != want {
		t.Errorf("the shell reads the command as\n%s\nwant\n%s", got, want)
	}

	var cfg map[string]map[string]stdioServer
	if err := json.Unmarshal([]byte(lineAfter(t, out, "Claude Desktop")), &cfg); err != nil {
		t.Fatal(err)
	}
	if s := cfg["mcpServers"]["limelit"]; s.Command != exe || s.Env["LIMELIT_DATA_DIR"] != data {
		t.Errorf("Claude Desktop entry = %+v", s)
	}
}

// TestConnectInAContainerGoesThroughDockerExec. Inside a container the
// binary's path means nothing on the host; Claude reaches it with docker
// exec, and the image already names the data directory.
func TestConnectInAContainerGoesThroughDockerExec(t *testing.T) {
	out := connectText(connectInfo{
		version: "v0.1.0", addr: tcpAddr(t, "[::]:1515"),
		exe: "/usr/local/bin/limelit", dataDir: "/data", container: "3f2a9c1b7d4e",
	})
	if !strings.Contains(out, "claude mcp add limelit -s user -- docker exec -i 3f2a9c1b7d4e limelit mcp") {
		t.Errorf("no docker exec command in:\n%s", out)
	}
	if strings.Contains(out, "/usr/local/bin/limelit") {
		t.Errorf("a path inside the container leaked into the host commands:\n%s", out)
	}
}

// TestBaseURLReachesAnyInterfaceAtLocalhost. A listener on every interface
// prints as localhost; one on a named address prints that address.
func TestBaseURLReachesAnyInterfaceAtLocalhost(t *testing.T) {
	for in, want := range map[string]string{
		"[::]:1515":         "http://localhost:1515",
		"0.0.0.0:1515":      "http://localhost:1515",
		"127.0.0.1:15151":   "http://127.0.0.1:15151",
		"[::1]:1515":        "http://[::1]:1515",
		"192.168.1.20:8080": "http://192.168.1.20:8080",
	} {
		if got := baseURL(tcpAddr(t, in)); got != want {
			t.Errorf("baseURL(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestConnectEndsWithWhatToAsk. Whatever the state, the message ends with the
// phrase to type in an assistant, so no one has to learn a tool name; the
// steps before it are only the ones this instance still needs.
func TestConnectEndsWithWhatToAsk(t *testing.T) {
	for _, tc := range []struct {
		state   string
		want    []string
		wantNot []string
	}{
		{mcpserver.StateNotSetUp, []string{"1. Finish setup at http://localhost:1515", "2. Press Run", "3. Then ask your assistant"}, nil},
		{mcpserver.StateSetupIncomplete, []string{"1. Finish setup", "3. Then ask your assistant"}, nil},
		{mcpserver.StateNeverRun, []string{"1. Press Run at http://localhost:1515", "2. Then ask your assistant"}, []string{"Finish setup"}},
		{mcpserver.StateStale, []string{"1. The last 30 days hold no answers", "2. Then ask your assistant"}, []string{"Finish setup"}},
		{mcpserver.StateReady, []string{"1. Ask your assistant"}, []string{"Press Run", "Finish setup"}},
		{"", []string{"1. Ask your assistant"}, nil},
	} {
		out := connectText(connectInfo{
			version: "v0.1.0", addr: tcpAddr(t, "[::]:1515"),
			exe: "/x/limelit", dataDir: "/x/data", state: tc.state,
		})
		next := out[strings.Index(out, "  Next\n"):]
		for _, w := range append(tc.want, `"Get started with Limelit"`, mcpserver.TryAsking[0]) {
			if !strings.Contains(next, w) {
				t.Errorf("state %q: Next block lacks %q:\n%s", tc.state, w, next)
			}
		}
		for _, w := range tc.wantNot {
			if strings.Contains(next, w) {
				t.Errorf("state %q: Next block should not say %q:\n%s", tc.state, w, next)
			}
		}
	}
}
