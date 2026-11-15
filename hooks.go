package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

const (
	HookProviders     = "kanban.providers"
	HookIssueFetch    = "kanban.issues.fetch"
	HookRunPrepare    = "kanban.run.prepare"
	HookCardSucceeded = "kanban.card.succeeded"
)

type RunContext struct {
	Kind   string            `json:"kind,omitempty"`
	Path   string            `json:"path,omitempty"`
	Branch string            `json:"branch,omitempty"`
	Base   string            `json:"base,omitempty"`
	Label  string            `json:"label,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
}

type RunPrepare struct {
	Card       Card       `json:"card"`
	Repo       string     `json:"repo"`
	BaseBranch string     `json:"base_branch,omitempty"`
	Context    RunContext `json:"context"`
}

type ProviderInfo struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Enabled       bool   `json:"enabled"`
	Available     bool   `json:"available"`
	Authenticated bool   `json:"authenticated"`
	Detail        string `json:"detail,omitempty"`
	Target        string `json:"target,omitempty"`
	SyncMinutes   int    `json:"sync_minutes,omitempty"`
}

type HookIssue struct {
	ExternalID string   `json:"external_id,omitempty"`
	Number     int      `json:"number,omitempty"`
	Key        string   `json:"key,omitempty"`
	Title      string   `json:"title"`
	Body       string   `json:"body,omitempty"`
	URL        string   `json:"url,omitempty"`
	Labels     []string `json:"labels,omitempty"`
	Assignees  []string `json:"assignees,omitempty"`
	State      string   `json:"state,omitempty"`
	Priority   string   `json:"priority,omitempty"`
	CreatedAt  string   `json:"created_at,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

type IssueFetch struct {
	Provider string      `json:"provider"`
	Target   string      `json:"target,omitempty"`
	Labels   []string    `json:"labels,omitempty"`
	State    string      `json:"state,omitempty"`
	Limit    int         `json:"limit,omitempty"`
	Query    string      `json:"query,omitempty"`
	Issues   []HookIssue `json:"issues"`
}

type CardSucceeded struct {
	Card                Card        `json:"card"`
	Context             *RunContext `json:"context,omitempty"`
	Summary             string      `json:"summary,omitempty"`
	Feedback            bool        `json:"feedback"`
	CreateChangeRequest bool        `json:"create_change_request"`
	TransitionOnSuccess string      `json:"transition_on_success,omitempty"`
	BaseBranch          string      `json:"base_branch,omitempty"`
}

type hookRunner interface {
	Apply(ctx context.Context, name string, kind hooks.HookKind, value any) (hooks.Outcome, error)
	FilterInto(ctx context.Context, name string, value, out any) (hooks.Outcome, error)
	Close() error
}

func newHooksClient(settings Settings) *hooks.Client {
	timeout := time.Duration(settings.HooksTimeout) * time.Second
	if v := strings.TrimSpace(os.Getenv("PEGG_HOOKS_TIMEOUT")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	return &hooks.Client{Timeout: timeout}
}

func (p *Plugin) cachedProviders() []ProviderInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ProviderInfo(nil), p.providerCache...)
}

func (p *Plugin) refreshProviderCache(ctx context.Context) {
	svc, err := p.localService()
	if err != nil {
		return
	}
	providers, err := svc.Providers(ctx)
	if err != nil {
		return
	}
	p.mu.Lock()
	p.providerCache = providers
	p.mu.Unlock()
}
