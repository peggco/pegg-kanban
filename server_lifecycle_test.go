package main

import (
	"net"
	"os"
	"testing"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func newLifecyclePlugin(t *testing.T, workdir string, port int) *Plugin {
	t.Helper()
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = workdir
	settings.Port = port
	settings.AutoOpen = false
	settings.PeggBinary = "/bin/false"
	settings.normalize()
	return &Plugin{settings: settings}
}

func TestStartServerInProcessAndClose(t *testing.T) {
	plugin := newLifecyclePlugin(t, t.TempDir(), freePort(t))

	url, err := plugin.startServer(false)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if health, err := healthCheck(plugin.currentSettings(), url); err != nil || !health.OK {
		t.Fatalf("health: %v %#v", err, health)
	}
	if !plugin.ownsServer {
		t.Fatal("plugin should own the server it started")
	}
	info, err := readServerInfo(plugin.currentSettings().serverPath())
	if err != nil {
		t.Fatal(err)
	}
	if !info.Embedded || info.PID != os.Getpid() || info.URL != url {
		t.Fatalf("server info = %#v", info)
	}

	if err := plugin.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, err := healthCheck(plugin.currentSettings(), url); err == nil {
		t.Fatal("server still healthy after close")
	}
	if _, err := os.Stat(plugin.currentSettings().serverPath()); !os.IsNotExist(err) {
		t.Fatalf("server metadata still present: %v", err)
	}
}

func TestStartServerReusesExistingServer(t *testing.T) {
	workdir := t.TempDir()
	port := freePort(t)
	owner := newLifecyclePlugin(t, workdir, port)
	peer := newLifecyclePlugin(t, workdir, port)

	url, err := owner.startServer(false)
	if err != nil {
		t.Fatalf("owner start: %v", err)
	}
	peerURL, err := peer.startServer(false)
	if err != nil {
		t.Fatalf("peer start: %v", err)
	}
	if peerURL != url {
		t.Fatalf("peer URL = %s, want %s", peerURL, url)
	}
	if peer.ownsServer {
		t.Fatal("peer should not own the existing server")
	}

	if err := peer.Close(); err != nil {
		t.Fatalf("peer close: %v", err)
	}
	if _, err := healthCheck(owner.currentSettings(), url); err != nil {
		t.Fatalf("peer close stopped the owner server: %v", err)
	}

	if err := owner.Close(); err != nil {
		t.Fatalf("owner close: %v", err)
	}
	if _, err := healthCheck(owner.currentSettings(), url); err == nil {
		t.Fatal("owner close left the server running")
	}
}
