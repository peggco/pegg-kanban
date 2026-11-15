package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestService(t *testing.T, tweak func(*Settings)) (*Service, *httptest.Server) {
	t.Helper()
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = t.TempDir()
	settings.Port = 0
	settings.AutoOpen = false
	settings.PeggBinary = "/bin/false"
	if tweak != nil {
		tweak(&settings)
	}
	svc, err := NewService(settings)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ts := httptest.NewServer(svc.handler())
	t.Cleanup(ts.Close)
	return svc, ts
}

func doJSON(t *testing.T, method, url string, payload any, headers map[string]string) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, data
}

func TestServerHealthAndCRUD(t *testing.T) {
	svc, ts := newTestService(t, nil)

	status, body := doJSON(t, http.MethodGet, ts.URL+"/api/health", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("health status = %d: %s", status, body)
	}
	var health map[string]any
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("health JSON: %v", err)
	}
	if health["ok"] != true || health["version"] != version {
		t.Fatalf("health = %v", health)
	}
	if health["board"] != svc.store.Path() {
		t.Fatalf("health board = %v, want %s", health["board"], svc.store.Path())
	}

	status, body = doJSON(t, http.MethodGet, ts.URL+"/api/board", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("board status = %d", status)
	}
	var board Board
	if err := json.Unmarshal(body, &board); err != nil {
		t.Fatal(err)
	}
	if len(board.Columns) != 5 {
		t.Fatalf("columns = %d", len(board.Columns))
	}

	status, body = doJSON(t, http.MethodGet, ts.URL+"/api/cards/does-not-exist", nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("missing card status = %d: %s", status, body)
	}

	status, body = doJSON(t, http.MethodPost, ts.URL+"/api/cards", map[string]any{
		"title":    "Ship it",
		"body":     "Do the thing",
		"column":   "ready",
		"priority": "high",
		"labels":   []string{"ui", "release"},
		"assignee": "dev",
	}, nil)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d: %s", status, body)
	}
	var card Card
	if err := json.Unmarshal(body, &card); err != nil {
		t.Fatal(err)
	}
	if card.ID == "" || card.Column != "ready" || card.Source != "manual" || card.Run.Status != RunIdle {
		t.Fatalf("created card = %+v", card)
	}

	status, body = doJSON(t, http.MethodGet, ts.URL+"/api/cards/"+card.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("get card = %d: %s", status, body)
	}

	status, body = doJSON(t, http.MethodPatch, ts.URL+"/api/cards/"+card.ID, map[string]any{
		"title":    "Ship it now",
		"priority": "urgent",
		"labels":   []string{"release"},
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("patch = %d: %s", status, body)
	}
	var patched Card
	_ = json.Unmarshal(body, &patched)
	if patched.Title != "Ship it now" || patched.Priority != "urgent" || len(patched.Labels) != 1 {
		t.Fatalf("patched card = %+v", patched)
	}

	status, body = doJSON(t, http.MethodPost, ts.URL+"/api/cards/"+card.ID+"/move", map[string]any{
		"column": "nope", "position": 0,
	}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid move status = %d: %s", status, body)
	}

	status, body = doJSON(t, http.MethodPost, ts.URL+"/api/cards/"+card.ID+"/move", map[string]any{
		"column": "review", "position": 0,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("move status = %d: %s", status, body)
	}
	var moved Card
	_ = json.Unmarshal(body, &moved)
	if moved.Column != "review" {
		t.Fatalf("moved card column = %s", moved.Column)
	}

	logPath := svc.store.LogPath(card.ID)
	if err := os.MkdirAll(svc.store.StateDir()+"/logs", 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, "line"+string(rune('0'+i)))
	}
	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, body = doJSON(t, http.MethodGet, ts.URL+"/api/cards/"+card.ID+"/log?tail=2", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("log status = %d", status)
	}
	if string(body) != "line4\nline5" {
		t.Fatalf("log tail = %q", body)
	}

	status, body = doJSON(t, http.MethodGet, ts.URL+"/api/config", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("config status = %d", status)
	}
	if bytes.Contains(body, []byte("api_token")) {
		t.Fatalf("config leaks api_token: %s", body)
	}

	status, body = doJSON(t, http.MethodDelete, ts.URL+"/api/cards/"+card.ID, nil, nil)
	if status != http.StatusOK {
		t.Fatalf("delete status = %d: %s", status, body)
	}
	status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/cards/"+card.ID, nil, nil)
	if status != http.StatusNotFound {
		t.Fatalf("deleted card status = %d", status)
	}

	if status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/unknown", nil, nil); status != http.StatusNotFound {
		t.Fatalf("unknown route status = %d", status)
	}
}

func TestServerAuthToken(t *testing.T) {
	_, ts := newTestService(t, func(settings *Settings) {
		settings.APIToken = "tok-123"
	})

	status, _ := doJSON(t, http.MethodGet, ts.URL+"/api/board", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", status)
	}
	status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/board", nil, map[string]string{"X-PegG-Token": "wrong"})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d", status)
	}
	status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/board", nil, map[string]string{"X-PegG-Token": "tok-123"})
	if status != http.StatusOK {
		t.Fatalf("header token status = %d", status)
	}
	status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/board", nil, map[string]string{"Authorization": "Bearer tok-123"})
	if status != http.StatusOK {
		t.Fatalf("bearer token status = %d", status)
	}
	status, _ = doJSON(t, http.MethodGet, ts.URL+"/api/board?token=tok-123", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("query token status = %d", status)
	}
}

func TestServerSSEFirstEvent(t *testing.T) {
	_, ts := newTestService(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open SSE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("SSE content type = %q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	var eventLine, dataLine string
	for i := 0; i < 4; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "event: ") {
			eventLine = line
		}
		if strings.HasPrefix(line, "data: ") {
			dataLine = line
			break
		}
	}
	if eventLine != "event: board" {
		t.Fatalf("first SSE event = %q", eventLine)
	}
	if !strings.HasPrefix(dataLine, "data: {") {
		t.Fatalf("SSE data = %q", dataLine)
	}
}

func TestConfigIncludesProviders(t *testing.T) {
	clearKanbanEnv(t)
	svc, ts := newTestService(t, nil)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{{
			ID:          "example",
			Label:       "Example",
			Enabled:     true,
			Available:   true,
			Target:      "acme/widgets",
			SyncMinutes: 9,
		}},
	}

	status, body := doJSON(t, http.MethodGet, ts.URL+"/api/config", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("config status = %d: %s", status, body)
	}
	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		t.Fatalf("config JSON: %v", err)
	}
	providers, ok := config["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("config providers = %#v", config["providers"])
	}
	provider := providers[0].(map[string]any)
	if provider["id"] != "example" || provider["target"] != "acme/widgets" {
		t.Fatalf("provider = %#v", provider)
	}
	if provider["sync_minutes"] != float64(9) {
		t.Fatalf("provider sync_minutes = %#v", provider["sync_minutes"])
	}
	for _, key := range []string{"github", "gitlab", "jira"} {
		if _, exists := config[key]; exists {
			t.Fatalf("config still exposes %q", key)
		}
	}
}

func TestSyncLoopStartsForAvailableProviders(t *testing.T) {
	clearKanbanEnv(t)
	svc, _ := newTestService(t, nil)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{{
			ID:          "example",
			Label:       "Example",
			Enabled:     true,
			Available:   true,
			SyncMinutes: 1,
		}},
	}

	svc.startSyncLoop(context.Background())
	svc.mu.Lock()
	stopSync := svc.stopSync
	svc.mu.Unlock()
	if stopSync == nil {
		t.Fatal("sync loop did not start for an available provider")
	}
	if err := svc.Stop(context.Background()); err != nil {
		t.Fatalf("stop service: %v", err)
	}
}

func TestServerOversizedBody(t *testing.T) {
	_, ts := newTestService(t, nil)
	big := strings.Repeat("x", 1<<20+1024)
	status, body := doJSON(t, http.MethodPost, ts.URL+"/api/cards", map[string]any{
		"title": big,
	}, nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d: %s", status, body)
	}
}
