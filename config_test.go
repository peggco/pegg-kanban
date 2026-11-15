package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearKanbanEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PEGG_KANBAN_HOST", "PEGG_KANBAN_PORT", "PEGG_KANBAN_TOKEN",
		"PEGG_KANBAN_MAX_PARALLEL", "PEGG_KANBAN_PEGG_BIN",
		"PEGG_KANBAN_BOARD", "PEGG_KANBAN_SERVER",
		"PEGG_KANBAN_WORKTREE_ROOT", "PEGG_KANBAN_BASE", "PEGG_KANBAN_AUTO_OPEN",
		"PEGG_KANBAN_SYNC_COLUMN", "PEGG_KANBAN_SYNC_MINUTES",
		"PEGG_KANBAN_SYNC_UPDATE_EXISTING", "PEGG_KANBAN_SYNC_LABELS",
		"PEGG_KANBAN_SYNC_LIMIT", "PEGG_KANBAN_SYNC_STATE",
		"PEGG_KANBAN_FEEDBACK", "PEGG_KANBAN_CREATE_CHANGE_REQUEST",
		"PEGG_KANBAN_TRANSITION_ON_SUCCESS",
		"PEGG_HOOKS_BIN", "PEGG_HOOKS_TIMEOUT", "PEGG_BIN",
	} {
		t.Setenv(key, "")
	}
}

func TestDefaultSettings(t *testing.T) {
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = t.TempDir()
	settings.normalize()

	if !settings.Enabled {
		t.Fatal("expected enabled by default")
	}
	if settings.Host != "127.0.0.1" || settings.Port != 7878 {
		t.Fatalf("host/port = %s/%d", settings.Host, settings.Port)
	}
	if settings.MaxParallel != 2 {
		t.Fatalf("max_parallel = %d", settings.MaxParallel)
	}
	if settings.TriggerColumn != "in_progress" || settings.ReviewColumn != "review" {
		t.Fatalf("columns = %s/%s", settings.TriggerColumn, settings.ReviewColumn)
	}
	if !settings.AutoMoveOnSuccess || !settings.AutoOpen {
		t.Fatal("expected auto move and auto open")
	}
	if settings.SyncColumn != "backlog" || settings.SyncLimit != 50 || settings.SyncMinutes != 0 {
		t.Fatalf("sync defaults: %+v", settings)
	}
	if !settings.SyncUpdateExisting || !settings.Feedback || settings.CreateChangeRequest {
		t.Fatalf("sync flags: %+v", settings)
	}
	if settings.PeggBinary == "" || settings.HooksTimeout <= 0 {
		t.Fatalf("hook settings: %+v", settings)
	}
	wantBoard := filepath.Join(settings.Workdir, ".pegg", "kanban", "board.json")
	if settings.boardPath() != wantBoard {
		t.Fatalf("board path = %s, want %s", settings.boardPath(), wantBoard)
	}
	if settings.logsDir() != filepath.Join(filepath.Dir(wantBoard), "logs") {
		t.Fatalf("logs dir = %s", settings.logsDir())
	}
}

func TestApplyHostOptions(t *testing.T) {
	clearKanbanEnv(t)
	settings := defaultSettings()
	config, _ := json.Marshal(map[string]any{
		"plugins": map[string]any{
			"options": map[string]any{
				"kanban": map[string]any{
					"port":                  9001,
					"max_parallel":          4,
					"trigger_column":        "doing",
					"sync_column":           "ready",
					"sync_labels":           []string{"bug", "help"},
					"auto_open":             false,
					"create_change_request": true,
				},
			},
		},
	})
	settings.applyHost(config)
	if settings.Port != 9001 || settings.MaxParallel != 4 {
		t.Fatalf("host options not applied: %+v", settings)
	}
	if settings.TriggerColumn != "doing" || settings.SyncColumn != "ready" {
		t.Fatalf("string options not applied: %+v", settings)
	}
	if len(settings.SyncLabels) != 2 || settings.AutoOpen || !settings.CreateChangeRequest {
		t.Fatalf("slice/bool options not applied: %+v", settings)
	}
}

func TestApplyEnv(t *testing.T) {
	clearKanbanEnv(t)
	dir := t.TempDir()
	board := filepath.Join(dir, "custom", "board.json")
	t.Setenv("PEGG_KANBAN_HOST", "localhost")
	t.Setenv("PEGG_KANBAN_PORT", "9999")
	t.Setenv("PEGG_KANBAN_TOKEN", "secret-token")
	t.Setenv("PEGG_KANBAN_MAX_PARALLEL", "5")
	t.Setenv("PEGG_KANBAN_PEGG_BIN", "/tmp/fake-pegg")
	t.Setenv("PEGG_KANBAN_SYNC_LABELS", "a, b ,c")
	t.Setenv("PEGG_KANBAN_SYNC_LIMIT", "17")
	t.Setenv("PEGG_KANBAN_BOARD", board)
	t.Setenv("PEGG_KANBAN_AUTO_OPEN", "false")

	settings := defaultSettings()
	settings.Workdir = dir
	settings.applyEnv()

	if settings.Host != "localhost" || settings.Port != 9999 {
		t.Fatalf("host/port env: %+v", settings)
	}
	if settings.APIToken != "secret-token" || settings.MaxParallel != 5 {
		t.Fatalf("token/parallel env: %+v", settings)
	}
	if settings.PeggBinary != "/tmp/fake-pegg" {
		t.Fatalf("binaries env: %+v", settings)
	}
	if strings.Join(settings.SyncLabels, ",") != "a,b,c" || settings.SyncLimit != 17 {
		t.Fatalf("sync env: %+v", settings)
	}
	if settings.AutoOpen {
		t.Fatal("auto_open env should disable auto open")
	}
	if settings.boardPath() != board {
		t.Fatalf("board path = %s, want %s", settings.boardPath(), board)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := expandHome("~/x/y"); got != filepath.Join(home, "x", "y") {
		t.Fatalf("expandHome = %s", got)
	}
	if got := expandHome("~"); got != home {
		t.Fatalf("expandHome ~ = %s", got)
	}
	if got := expandHome("/absolute/path"); got != "/absolute/path" {
		t.Fatalf("absolute path changed: %s", got)
	}
	if got := expandHome(""); got != "" {
		t.Fatalf("empty path = %q", got)
	}
}

func TestSanitizedConfigHidesToken(t *testing.T) {
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = t.TempDir()
	settings.APIToken = "super-secret-value"
	out := settings.sanitized()
	if _, exists := out["api_token"]; exists {
		t.Fatal("sanitized config exposes api_token")
	}
	if out["token_configured"] != true {
		t.Fatal("sanitized config should report token_configured")
	}
	for _, key := range []string{"github", "gitlab", "jira", "github_repo", "gitlab_project", "jira_project"} {
		if _, exists := out[key]; exists {
			t.Fatalf("sanitized config still exposes %q", key)
		}
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "super-secret-value") {
		t.Fatal("sanitized config leaks the token")
	}
}
