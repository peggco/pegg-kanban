package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

func newTUIPlugin(t *testing.T, tweak func(*Settings), fake *fakeHookRunner) *Plugin {
	t.Helper()
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = t.TempDir()
	settings.AutoOpen = false
	settings.PeggBinary = "/bin/false"
	if tweak != nil {
		tweak(&settings)
	}
	settings.normalize()
	plugin := &Plugin{settings: settings}
	if fake == nil {
		fake = &fakeHookRunner{}
	}
	svc, err := plugin.localService()
	if err != nil {
		t.Fatalf("local service: %v", err)
	}
	svc.hookRunner = fake
	t.Cleanup(func() {
		plugin.mu.Lock()
		svc := plugin.service
		plugin.service = nil
		plugin.mu.Unlock()
		if svc != nil {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = svc.Stop(ctx)
		}
	})
	return plugin
}

func tuiSection(t *testing.T, page peggplugin.TUIPage, title string) peggplugin.TUISection {
	t.Helper()
	for _, section := range page.Sections {
		if section.Title == title {
			return section
		}
	}
	t.Fatalf("section %q not found", title)
	return peggplugin.TUISection{}
}

func tuiItem(t *testing.T, section peggplugin.TUISection, title string) peggplugin.TUIItem {
	t.Helper()
	for _, item := range section.Items {
		if item.Title == title {
			return item
		}
	}
	t.Fatalf("item %q not found in section %q", title, section.Title)
	return peggplugin.TUIItem{}
}

func tuiAction(t *testing.T, page peggplugin.TUIPage, id string) peggplugin.TUIAction {
	t.Helper()
	for _, action := range page.Actions {
		if action.ID == id {
			return action
		}
	}
	t.Fatalf("action %q not found", id)
	return peggplugin.TUIAction{}
}

func TestKanbanTUIPageReflectsProviders(t *testing.T) {
	plugin := newTUIPlugin(t, nil, &fakeHookRunner{
		providers: []ProviderInfo{
			{ID: "example", Label: "Example", Enabled: true, Available: true, Target: "acme/widgets", SyncMinutes: 5},
			{ID: "disabled", Label: "Disabled", Enabled: false, Detail: "disabled in plugin settings"},
		},
	})
	plugin.refreshProviderCache(context.Background())
	pages, err := plugin.TUIPages()
	if err != nil {
		t.Fatalf("TUIPages: %v", err)
	}
	if len(pages) != 1 || pages[0].ID != kanbanTUIPageID || pages[0].Title != "Kanban" {
		t.Fatalf("pages = %#v", pages)
	}
	page := pages[0]

	server := tuiSection(t, page, "Server")
	if status := tuiItem(t, server, "Status"); status.Detail != "stopped" {
		t.Fatalf("server status = %#v", status)
	}
	if board := tuiItem(t, server, "Board"); !strings.HasSuffix(board.Detail, filepath.Join(".pegg", "kanban", "board.json")) {
		t.Fatalf("board = %#v", board)
	}

	providers := tuiSection(t, page, "Providers")
	example := tuiItem(t, providers, "Example")
	if example.Badge != "ready" || !strings.Contains(example.Detail, "acme/widgets") || !strings.Contains(example.Detail, "every 5m") {
		t.Fatalf("example provider = %#v", example)
	}
	disabled := tuiItem(t, providers, "Disabled")
	if disabled.Badge != "disabled" || !strings.Contains(disabled.Detail, "disabled in plugin settings") {
		t.Fatalf("disabled provider = %#v", disabled)
	}

	sync := tuiAction(t, page, "sync")
	fields := map[string]peggplugin.TUIField{}
	for _, field := range sync.Fields {
		fields[field.ID] = field
	}
	if got := strings.Join(fields["provider"].Options, ","); got != "all,example,disabled" {
		t.Fatalf("provider options = %q", got)
	}
	for _, id := range []string{"query", "labels", "state", "limit"} {
		if _, ok := fields[id]; !ok {
			t.Fatalf("sync action missing field %q", id)
		}
	}

	settings := map[string]string{}
	for _, field := range page.Settings {
		settings[field.ID] = field.Value
	}
	for _, id := range []string{"sync_column", "sync_minutes", "sync_labels", "sync_limit", "feedback", "create_change_request", "transition_on_success"} {
		if _, ok := settings[id]; !ok {
			t.Fatalf("settings missing field %q", id)
		}
	}
	for _, id := range []string{"github_repo", "gitlab_project", "jira_project"} {
		if _, ok := settings[id]; ok {
			t.Fatalf("settings still exposes %q", id)
		}
	}

	runner := tuiSection(t, page, "Runner")
	if max := tuiItem(t, runner, "Max parallel"); max.Detail != "2" {
		t.Fatalf("max parallel = %#v", max)
	}
}

func TestKanbanTUIActions(t *testing.T) {
	fake := &fakeHookRunner{
		providers: []ProviderInfo{{
			ID:        "example",
			Label:     "Example",
			Enabled:   true,
			Available: true,
			Target:    "acme/widgets",
		}},
		issues: map[string][]HookIssue{
			"example": {{Number: 3, Title: "Imported", URL: "https://example.test/issues/3"}},
		},
	}
	plugin := newTUIPlugin(t, nil, fake)

	result, err := plugin.ExecuteTUIAction(peggplugin.TUIActionRequest{PageID: kanbanTUIPageID, ActionID: "stop"})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !strings.Contains(result.Output, "not running") {
		t.Fatalf("stop output = %q", result.Output)
	}
	if result.Page == nil || result.Page.ID != kanbanTUIPageID {
		t.Fatalf("stop page = %#v", result.Page)
	}

	result, err = plugin.ExecuteTUIAction(peggplugin.TUIActionRequest{PageID: kanbanTUIPageID, ActionID: "refresh"})
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if result.Output != "Refreshed" || result.Page == nil {
		t.Fatalf("refresh result = %#v", result)
	}

	result, err = plugin.ExecuteTUIAction(peggplugin.TUIActionRequest{
		PageID:   kanbanTUIPageID,
		ActionID: "sync",
		Values:   map[string]string{"provider": "example", "target": "acme/widgets"},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !strings.Contains(result.Output, "Synced acme/widgets") {
		t.Fatalf("sync output = %q", result.Output)
	}

	if _, err := plugin.ExecuteTUIAction(peggplugin.TUIActionRequest{PageID: kanbanTUIPageID, ActionID: "nope"}); err == nil {
		t.Fatal("expected unknown action error")
	}
	if _, err := plugin.ExecuteTUIAction(peggplugin.TUIActionRequest{PageID: "other", ActionID: "refresh"}); err == nil {
		t.Fatal("expected unknown page error")
	}
}
