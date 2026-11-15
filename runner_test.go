package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFakePegg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pegg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestRunnerService(t *testing.T, repo, script string) *Service {
	t.Helper()
	clearKanbanEnv(t)
	settings := defaultSettings()
	settings.Workdir = repo
	settings.Port = 0
	settings.AutoOpen = false
	settings.PeggBinary = script
	settings.MaxParallel = 1
	settings.AutoMoveOnSuccess = true
	svc, err := NewService(settings)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	svc.hookRunner = &fakeHookRunner{}
	t.Cleanup(svc.runner.Shutdown)
	return svc
}

func triggerCard(t *testing.T, svc *Service, card *Card) {
	t.Helper()
	if err := svc.runner.Trigger(card.ID); err != nil {
		t.Fatalf("trigger: %v", err)
	}
}

func waitRunStatus(t *testing.T, store *Store, id, want string, timeout time.Duration) *Card {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *Card
	for time.Now().Before(deadline) {
		card, err := store.Card(id)
		if err == nil {
			last = card
			if card.Run.Status == want {
				return card
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last == nil {
		t.Fatalf("timed out waiting for %s; card not found", want)
	}
	t.Fatalf("timed out waiting for run status %s; last = %s (error %q)", want, last.Run.Status, last.Run.Error)
	return nil
}

func addTriggerCard(t *testing.T, svc *Service, title string) *Card {
	t.Helper()
	card := Card{
		Title:  title,
		Column: "in_progress",
		Run:    RunState{Status: RunIdle},
	}
	if _, err := svc.store.Add(card); err != nil {
		t.Fatalf("add card: %v", err)
	}
	board, _ := svc.store.Load()
	for i := range board.Cards {
		if board.Cards[i].Title == title {
			return &board.Cards[i]
		}
	}
	t.Fatalf("card %q not found", title)
	return nil
}

func TestRunnerSuccessRunsInRepoRootAndMovesCard(t *testing.T) {
	repo := initTestRepo(t)
	script := writeFakePegg(t, `echo "fake pegg starting"
echo "card=$PEGG_KANBAN_CARD"
echo "board=$PEGG_KANBAN_BOARD"
echo "$PEGG_KANBAN_CARD" > card-id.txt
pwd > worktree-path.txt
echo "all done"
exit 0
`)
	svc := newTestRunnerService(t, repo, script)
	card := addTriggerCard(t, svc, "Success card")
	triggerCard(t, svc, card)

	final := waitRunStatus(t, svc.store, card.ID, RunSucceeded, 15*time.Second)
	if final.Column != "review" {
		t.Fatalf("card column = %s, want review", final.Column)
	}
	if final.Run.ExitCode != 0 || final.Run.FinishedAt == "" {
		t.Fatalf("run state = %+v", final.Run)
	}
	if final.Context != nil {
		t.Fatalf("card should not have a run context by default: %#v", final.Context)
	}

	logData, err := os.ReadFile(final.Run.Log)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), "fake pegg starting") || !strings.Contains(string(logData), "all done") {
		t.Fatalf("log = %q", logData)
	}

	cardFile, err := os.ReadFile(filepath.Join(repo, "card-id.txt"))
	if err != nil {
		t.Fatalf("fake agent file missing: %v", err)
	}
	if strings.TrimSpace(string(cardFile)) != card.ID {
		t.Fatalf("PEGG_KANBAN_CARD = %q, want %q", cardFile, card.ID)
	}
	worktreeFile, err := os.ReadFile(filepath.Join(repo, "worktree-path.txt"))
	if err != nil {
		t.Fatalf("worktree marker missing: %v", err)
	}
	if root, _ := gitRepoRoot(repo); strings.TrimSpace(string(worktreeFile)) != strings.TrimSpace(root) {
		t.Fatalf("cmd.Dir = %q, want %q", worktreeFile, root)
	}
}

func TestRunnerFailureStaysInTriggerColumn(t *testing.T) {
	repo := initTestRepo(t)
	script := writeFakePegg(t, `echo "about to fail"
echo "boom failure details"
exit 3
`)
	svc := newTestRunnerService(t, repo, script)
	card := addTriggerCard(t, svc, "Failure card")
	triggerCard(t, svc, card)

	final := waitRunStatus(t, svc.store, card.ID, RunFailed, 15*time.Second)
	if final.Column != "in_progress" {
		t.Fatalf("failed card moved to %s", final.Column)
	}
	if final.Run.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", final.Run.ExitCode)
	}
	if !strings.Contains(final.Run.Error, "boom failure details") {
		t.Fatalf("run error = %q", final.Run.Error)
	}
}

func TestRunnerCancelKillsProcessGroup(t *testing.T) {
	repo := initTestRepo(t)
	script := writeFakePegg(t, `trap 'echo got-term; exit 143' TERM
echo "ready to sleep"
while true; do sleep 1; done
`)
	svc := newTestRunnerService(t, repo, script)
	card := addTriggerCard(t, svc, "Cancel card")
	triggerCard(t, svc, card)
	waitRunStatus(t, svc.store, card.ID, RunRunning, 15*time.Second)

	if err := svc.runner.Cancel(card.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	final := waitRunStatus(t, svc.store, card.ID, RunCancelled, 10*time.Second)
	if final.Column != "in_progress" {
		t.Fatalf("cancelled card column = %s", final.Column)
	}
}

func TestRunnerMarkInterrupted(t *testing.T) {
	repo := initTestRepo(t)
	svc := newTestRunnerService(t, repo, writeFakePegg(t, "exit 0\n"))
	card := addTriggerCard(t, svc, "Interrupted card")
	if _, _, err := svc.store.SetRun(card.ID, func(run *RunState) {
		run.Status = RunRunning
		run.PID = 999999
		run.StartedAt = nowRFC3339()
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.runner.MarkInterrupted(); err != nil {
		t.Fatalf("mark interrupted: %v", err)
	}
	final, err := svc.store.Card(card.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Run.Status != RunFailed || !strings.Contains(final.Run.Error, "interrupted by server restart") {
		t.Fatalf("run after restart = %+v", final.Run)
	}
	if final.Run.FinishedAt == "" {
		t.Fatal("finished_at not set")
	}
}

func TestBuildPromptIncludesCardContext(t *testing.T) {
	card := Card{
		ID:       "card-1",
		Title:    "Fix login",
		Body:     "The login form is broken.",
		Column:   "in_progress",
		Priority: "high",
		Labels:   []string{"bug", "auth"},
		Issue:    &IssueRef{Provider: "github", Repo: "octo/repo", Number: 42, URL: "https://github.com/octo/repo/issues/42"},
	}
	runContext := &RunContext{Kind: "worktree", Path: "/repo/.pegg-worktrees/fix-login", Branch: "pegg/fix-login", Label: "fix-login"}
	prompt := buildPrompt(card, "/repo", runContext)
	for _, want := range []string{
		"Fix login (card-1)",
		"Priority: high",
		"Labels: bug, auth",
		"Column: in_progress",
		"The login form is broken.",
		"Repository: /repo",
		"Environment: worktree",
		"Path: /repo/.pegg-worktrees/fix-login",
		"Branch: pegg/fix-login",
		"github issue: octo/repo#42 https://github.com/octo/repo/issues/42",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	noIssue := buildPrompt(Card{ID: "card-2", Title: "Plain", Column: "in_progress"}, "/repo", nil)
	if strings.Contains(noIssue, "issue:") {
		t.Fatal("prompt should not include an issue block without an issue")
	}
}

func TestRunnerAutoAnswerFeedsStdin(t *testing.T) {
	repo := initTestRepo(t)
	script := writeFakePegg(t, `read answer
echo "$answer" > stdin-answer.txt
exit 0
`)
	svc := newTestRunnerService(t, repo, script)
	card := addTriggerCard(t, svc, "Auto answer card")
	triggerCard(t, svc, card)

	final := waitRunStatus(t, svc.store, card.ID, RunSucceeded, 15*time.Second)
	if final.Run.Status != RunSucceeded {
		t.Fatalf("run status = %s", final.Run.Status)
	}
	data, err := os.ReadFile(filepath.Join(repo, "stdin-answer.txt"))
	if err != nil {
		t.Fatalf("read stdin answer: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "1" {
		t.Fatalf("stdin answer = %q, want 1", got)
	}
}

func TestRunnerUsesHookProvidedRunContext(t *testing.T) {
	repo := initTestRepo(t)
	script := writeFakePegg(t, `pwd > cwd.txt
exit 0
`)
	svc := newTestRunnerService(t, repo, script)
	worktreePath := t.TempDir()
	fake := &fakeHookRunner{runContext: &RunContext{
		Kind:   "worktree",
		Path:   worktreePath,
		Branch: "pegg/card-worktree",
		Base:   "main",
		Label:  "card-worktree",
	}}
	svc.hookRunner = fake
	card := addTriggerCard(t, svc, "Worktree card")
	triggerCard(t, svc, card)

	final := waitRunStatus(t, svc.store, card.ID, RunSucceeded, 15*time.Second)
	if final.Context == nil || final.Context.Path != worktreePath || final.Context.Branch != "pegg/card-worktree" {
		t.Fatalf("context = %#v", final.Context)
	}
	data, err := os.ReadFile(filepath.Join(worktreePath, "cwd.txt"))
	if err != nil {
		t.Fatalf("read cwd: %v", err)
	}
	if strings.TrimSpace(string(data)) != worktreePath {
		t.Fatalf("cwd = %q, want %q", data, worktreePath)
	}
}
