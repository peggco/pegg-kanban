package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

type fakeHookRunner struct {
	providers  []ProviderInfo
	issues     map[string][]HookIssue
	fetchErr   map[string]string
	routes     map[string]string
	succeeded  []CardSucceeded
	runContext *RunContext
	applyErr   error
}

func (f *fakeHookRunner) Apply(ctx context.Context, name string, kind hooks.HookKind, value any) (hooks.Outcome, error) {
	if f.applyErr != nil {
		return hooks.Outcome{}, f.applyErr
	}
	if name == HookCardSucceeded {
		var payload CardSucceeded
		raw, _ := json.Marshal(value)
		_ = json.Unmarshal(raw, &payload)
		f.succeeded = append(f.succeeded, payload)
	}
	return hooks.Outcome{}, nil
}

func (f *fakeHookRunner) Close() error { return nil }

func (f *fakeHookRunner) FilterInto(ctx context.Context, name string, value, out any) (hooks.Outcome, error) {
	raw, _ := json.Marshal(value)
	switch name {
	case HookProviders:
		var list []ProviderInfo
		_ = json.Unmarshal(raw, &list)
		list = append(list, f.providers...)
		data, _ := json.Marshal(list)
		_ = json.Unmarshal(data, out)
		return hooks.Outcome{Value: data}, nil
	case HookRunPrepare:
		if f.runContext != nil {
			var req RunPrepare
			_ = json.Unmarshal(raw, &req)
			req.Context = *f.runContext
			data, _ := json.Marshal(req)
			_ = json.Unmarshal(data, out)
			return hooks.Outcome{Value: data}, nil
		}
		_ = json.Unmarshal(raw, out)
		return hooks.Outcome{Value: raw}, nil
	case HookIssueFetch:
		var req IssueFetch
		_ = json.Unmarshal(raw, &req)
		if message := f.fetchErr[req.Provider]; message != "" {
			data, _ := json.Marshal(req)
			return hooks.Outcome{Value: data, Errors: []string{message}}, nil
		}
		req.Issues = append(req.Issues, f.issues[req.Provider]...)
		data, _ := json.Marshal(req)
		_ = json.Unmarshal(data, out)
		return hooks.Outcome{Value: data}, nil
	case HookChangeRequestRoutes:
		routes := map[string]string{}
		_ = json.Unmarshal(raw, &routes)
		for key, value := range f.routes {
			routes[key] = value
		}
		data, _ := json.Marshal(routes)
		_ = json.Unmarshal(data, out)
		return hooks.Outcome{Value: data}, nil
	}
	return hooks.Outcome{}, nil
}

func newHooksService(t *testing.T) *Service {
	t.Helper()
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = t.TempDir()
	settings.Port = 0
	settings.AutoOpen = false
	settings.PeggBinary = "/bin/false"
	settings.SyncUpdateExisting = true
	svc, err := NewService(settings)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(svc.runner.Shutdown)
	return svc
}

func TestProvidersFromHook(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{
			{ID: "one", Label: "One", Enabled: true, Available: true, Target: "acme/one"},
			{ID: "two", Label: "Two", Enabled: true, Available: false, Detail: "not configured"},
		},
	}
	providers, err := svc.Providers(context.Background())
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if len(providers) != 2 || providers[0].ID != "one" || providers[1].Detail != "not configured" {
		t.Fatalf("providers = %#v", providers)
	}
}

func TestSyncProviderImportsAndUpdates(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{{
			ID:        "example",
			Label:     "Example",
			Enabled:   true,
			Available: true,
			Target:    "acme/widgets",
		}},
		issues: map[string][]HookIssue{
			"example": {{
				Number: 7,
				Title:  "Fix login",
				Body:   "broken",
				URL:    "https://example.test/issues/7",
				Labels: []string{"bug"},
			}},
		},
	}

	stats, err := svc.SyncProvider(context.Background(), SyncRequest{Provider: "example"})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if stats.Imported != 1 || stats.Total != 1 || stats.Target != "acme/widgets" {
		t.Fatalf("first stats = %#v", stats)
	}
	board, err := svc.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Cards) != 1 {
		t.Fatalf("cards = %d", len(board.Cards))
	}
	card := board.Cards[0]
	if card.Source != "example" || card.Issue == nil || card.Issue.Provider != "example" || card.Issue.Number != 7 {
		t.Fatalf("card = %#v", card)
	}
	if board.Sync["example"].LastStatus != "ok" || board.Sync["example"].Target != "acme/widgets" {
		t.Fatalf("sync state = %#v", board.Sync["example"])
	}

	svc.hookRunner.(*fakeHookRunner).issues["example"][0].Title = "Fix login properly"
	stats, err = svc.SyncProvider(context.Background(), SyncRequest{Provider: "example"})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if stats.Imported != 0 || stats.Updated != 1 {
		t.Fatalf("second stats = %#v", stats)
	}
	updated, err := svc.store.Card(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Title != "Fix login properly" {
		t.Fatalf("card title = %q", updated.Title)
	}
}

func TestSyncAllProvidersIsolatesErrors(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{
			{ID: "good", Label: "Good", Enabled: true, Available: true, Target: "acme/good"},
			{ID: "bad", Label: "Bad", Enabled: true, Available: true, Target: "acme/bad"},
		},
		issues: map[string][]HookIssue{
			"good": {{Number: 1, Title: "Good issue"}},
		},
		fetchErr: map[string]string{"bad": "upstream unavailable"},
	}
	result, err := svc.SyncAllProviders(context.Background(), SyncRequest{})
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if result.Results["good"].Imported != 1 {
		t.Fatalf("good result = %#v", result.Results)
	}
	if result.Errors["bad"] != "upstream unavailable" {
		t.Fatalf("bad error = %#v", result.Errors)
	}
}

func TestRunSuccessHookPayload(t *testing.T) {
	svc := newHooksService(t)
	fake := &fakeHookRunner{}
	svc.hookRunner = fake
	svc.settings.Feedback = true
	svc.settings.CreateChangeRequest = true
	svc.settings.TransitionOnSuccess = "Done"
	svc.settings.BaseBranch = "develop"

	card := &Card{
		ID:    "card-hook",
		Title: "Ship it",
		Issue: &IssueRef{Provider: "example", Repo: "acme/widgets", Number: 4, URL: "https://example.test/issues/4"},
	}
	runContext := &RunContext{Kind: "worktree", Path: "/tmp/wt", Branch: "pegg/ship-it", Base: "main", Label: "ship-it"}
	svc.runSuccessHooks(card, runContext)

	if len(fake.succeeded) != 1 {
		t.Fatalf("succeeded hooks = %d", len(fake.succeeded))
	}
	payload := fake.succeeded[0]
	if payload.Card.ID != card.ID || payload.Context == nil || payload.Context.Branch != "pegg/ship-it" {
		t.Fatalf("payload = %#v", payload)
	}
	if !payload.Feedback || !payload.CreateChangeRequest || payload.TransitionOnSuccess != "Done" || payload.BaseBranch != "develop" {
		t.Fatalf("payload flags = %#v", payload)
	}
	if payload.ChangeRequestProvider != "example" {
		t.Fatalf("change request provider = %q, want example", payload.ChangeRequestProvider)
	}
}

func TestBoardMigratesLegacySync(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	legacy := `{
		"version": 1,
		"columns": [{"id":"backlog","name":"Backlog"}],
		"cards": [],
		"github": {"last_sync":"2026-01-01T00:00:00Z","last_status":"ok","imported":3,"repo":"acme/widgets"},
		"gitlab": {"last_sync":"2026-01-02T00:00:00Z","last_status":"error","last_error":"boom","project":"group/project"}
	}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	board, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if board.Sync["github"].Imported != 3 || board.Sync["github"].Target != "acme/widgets" {
		t.Fatalf("github migration = %#v", board.Sync["github"])
	}
	if board.Sync["gitlab"].LastError != "boom" || board.Sync["gitlab"].Target != "group/project" {
		t.Fatalf("gitlab migration = %#v", board.Sync["gitlab"])
	}
}

func TestProvidersRequireRepoForRepoTarget(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{
		providers: []ProviderInfo{
			{ID: "git", Enabled: true, Available: true, RepoTarget: true},
			{ID: "ticket", Enabled: true, Available: true, Target: "PROJ"},
		},
	}
	providers, err := svc.Providers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if providers[0].Available {
		t.Fatal("repo-target provider should be unavailable without a git repository")
	}
	if !providers[1].Available {
		t.Fatal("provider with an explicit target should stay available")
	}
}

func TestSyncProviderRequiresRepo(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{}
	if _, err := svc.syncProvider(context.Background(), ProviderInfo{ID: "git", Enabled: true, Available: true}, SyncRequest{}); err == nil {
		t.Fatal("expected sync to fail without a git repository")
	}
}
