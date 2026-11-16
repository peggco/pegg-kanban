package main

import (
	"context"
	"testing"
)

func TestResolveChangeRequestProviderDefaultsToIssueProvider(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{}
	card := &Card{Issue: &IssueRef{Provider: "gitlab", Repo: "group/project", Number: 1}}
	if got := svc.resolveChangeRequestProvider(context.Background(), card); got != "gitlab" {
		t.Fatalf("provider = %q, want gitlab", got)
	}
}

func TestResolveChangeRequestProviderUsesPluginRoutes(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{routes: map[string]string{"jira": "bitbucket"}}
	card := &Card{Issue: &IssueRef{Provider: "jira", Key: "ABC-1"}}
	if got := svc.resolveChangeRequestProvider(context.Background(), card); got != "bitbucket" {
		t.Fatalf("provider = %q, want bitbucket", got)
	}
}

func TestResolveChangeRequestProviderUserOverrideWins(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{routes: map[string]string{"jira": "bitbucket"}}
	svc.settings.ChangeRequestProviders = map[string]string{"jira": "gitlab"}
	card := &Card{Issue: &IssueRef{Provider: "jira", Key: "ABC-1"}}
	if got := svc.resolveChangeRequestProvider(context.Background(), card); got != "gitlab" {
		t.Fatalf("provider = %q, want gitlab", got)
	}
}

func TestResolveChangeRequestProviderExplicitDisable(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{routes: map[string]string{"jira": "bitbucket"}}
	svc.settings.ChangeRequestProviders = map[string]string{"jira": ""}
	card := &Card{Issue: &IssueRef{Provider: "jira", Key: "ABC-1"}}
	if got := svc.resolveChangeRequestProvider(context.Background(), card); got != "" {
		t.Fatalf("provider = %q, want empty", got)
	}
}

func TestResolveChangeRequestProviderNoIssueUsesDefault(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{routes: map[string]string{"jira": "bitbucket"}}
	svc.settings.ChangeRequestProvider = "bitbucket"
	if got := svc.resolveChangeRequestProvider(context.Background(), &Card{}); got != "bitbucket" {
		t.Fatalf("provider = %q, want bitbucket", got)
	}
}

func TestResolveChangeRequestProviderCaseInsensitive(t *testing.T) {
	svc := newHooksService(t)
	svc.hookRunner = &fakeHookRunner{routes: map[string]string{"jira": "bitbucket"}}
	card := &Card{Issue: &IssueRef{Provider: "Jira", Key: "ABC-1"}}
	if got := svc.resolveChangeRequestProvider(context.Background(), card); got != "bitbucket" {
		t.Fatalf("provider = %q, want bitbucket", got)
	}
}
