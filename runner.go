package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const promptTemplate = `/goal You are working on a Kanban card.

Card: {{title}} ({{id}})
Priority: {{priority}}
Labels: {{labels}}
Column: {{column}}

Description:
{{body}}

Repository: {{repo}}
{{environment_block}}
{{issue_block}}

Instructions:
- {{environment_instruction}}
- Implement the task described on the card. If it came from a tracked issue, satisfy the issue.
- Run the relevant tests and fix failures you introduce.
- Commit your changes with a clear conventional-commit message.
- Push the branch to origin if a remote is configured.
- If this card has a tracked issue and the target is a git host, create a change request and reference the issue when appropriate.
- End with a short summary: changes, tests, files, follow-ups.
`

type runProc struct {
	cardID    string
	cancel    chan struct{}
	cancelOne sync.Once
	mu        sync.Mutex
	cmd       *exec.Cmd
	cancelled bool
	done      chan struct{}
}

func (p *runProc) requestCancel() {
	p.cancelOne.Do(func() {
		p.mu.Lock()
		p.cancelled = true
		p.mu.Unlock()
		close(p.cancel)
	})
}

func (p *runProc) isCancelled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cancelled
}

type Runner struct {
	store      *Store
	settings   Settings
	sem        chan struct{}
	mu         sync.Mutex
	running    map[string]*runProc
	onChange   func()
	onSuccess  func(*Card, *RunContext)
	prepareRun func(context.Context, *Card, string) (*RunContext, error)
	serverURL  string
}

func NewRunner(store *Store, settings Settings) *Runner {
	max := settings.MaxParallel
	if max <= 0 {
		max = 2
	}
	return &Runner{
		store:    store,
		settings: settings,
		sem:      make(chan struct{}, max),
		running:  map[string]*runProc{},
	}
}

func (r *Runner) SetOnChange(fn func()) {
	r.mu.Lock()
	r.onChange = fn
	r.mu.Unlock()
}

func (r *Runner) SetOnSuccess(fn func(*Card, *RunContext)) {
	r.mu.Lock()
	r.onSuccess = fn
	r.mu.Unlock()
}

func (r *Runner) SetPrepareRun(fn func(context.Context, *Card, string) (*RunContext, error)) {
	r.mu.Lock()
	r.prepareRun = fn
	r.mu.Unlock()
}

func (r *Runner) SetServerURL(url string) {
	r.mu.Lock()
	r.serverURL = strings.TrimRight(url, "/")
	r.mu.Unlock()
}

func (r *Runner) notify() {
	r.mu.Lock()
	fn := r.onChange
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (r *Runner) Trigger(cardID string) error {
	card, err := r.store.Card(cardID)
	if err != nil {
		return err
	}
	board, err := r.store.Load()
	if err != nil {
		return err
	}
	if card.Column != r.triggerColumn(board) {
		return fmt.Errorf("card %s is in column %q, not the trigger column %q", cardID, card.Column, r.triggerColumn(board))
	}

	r.mu.Lock()
	if _, exists := r.running[cardID]; exists {
		r.mu.Unlock()
		return nil
	}
	p := &runProc{
		cardID: cardID,
		cancel: make(chan struct{}),
		done:   make(chan struct{}),
	}
	r.running[cardID] = p
	r.mu.Unlock()

	_, _, err = r.store.SetRun(cardID, func(rs *RunState) {
		*rs = RunState{
			Status: RunQueued,
			Log:    r.store.LogPath(cardID),
		}
	})
	if err != nil {
		r.forget(p)
		return err
	}
	go r.execute(p)
	r.notify()
	return nil
}

func (r *Runner) Retry(cardID string) error {
	card, err := r.store.Card(cardID)
	if err != nil {
		return err
	}
	board, err := r.store.Load()
	if err != nil {
		return err
	}
	trigger := r.triggerColumn(board)
	if card.Column != trigger {
		if _, _, moveErr := r.store.Move(cardID, trigger, -1); moveErr != nil {
			return moveErr
		}
	}
	return r.Trigger(cardID)
}

func (r *Runner) Cancel(cardID string) error {
	r.mu.Lock()
	p := r.running[cardID]
	r.mu.Unlock()
	if p == nil {
		return fmt.Errorf("card %s is not queued or running", cardID)
	}
	p.requestCancel()

	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
		return nil
	}

	signalProcessGroup(cmd.Process.Pid, false)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		signalProcessGroup(cmd.Process.Pid, true)
		select {
		case <-p.done:
		case <-time.After(2 * time.Second):
		}
	}
	_, _, _ = r.store.SetRun(cardID, func(rs *RunState) {
		if rs.Status == RunRunning || rs.Status == RunQueued {
			rs.Status = RunCancelled
			rs.FinishedAt = nowRFC3339()
			rs.Error = "cancelled"
		}
	})
	r.notify()
	return nil
}

func (r *Runner) MarkInterrupted() error {
	_, err := r.store.Update(func(b *Board) error {
		for i := range b.Cards {
			status := b.Cards[i].Run.Status
			if status != RunRunning && status != RunQueued {
				continue
			}
			b.Cards[i].Run.Status = RunFailed
			b.Cards[i].Run.Error = "interrupted by server restart"
			b.Cards[i].Run.FinishedAt = nowRFC3339()
			b.Cards[i].UpdatedAt = nowRFC3339()
		}
		return nil
	})
	return err
}

func (r *Runner) Shutdown() {
	r.mu.Lock()
	procs := make([]*runProc, 0, len(r.running))
	for _, p := range r.running {
		procs = append(procs, p)
	}
	r.mu.Unlock()
	for _, p := range procs {
		p.requestCancel()
		p.mu.Lock()
		cmd := p.cmd
		p.mu.Unlock()
		if cmd != nil && cmd.Process != nil {
			signalProcessGroup(cmd.Process.Pid, false)
		}
	}
	deadline := time.After(3 * time.Second)
	for _, p := range procs {
		select {
		case <-p.done:
		case <-deadline:
			return
		}
	}
}

func (r *Runner) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.running))
	for id := range r.running {
		out = append(out, id)
	}
	return out
}

func (r *Runner) execute(p *runProc) {
	var runContext *RunContext
	var card Card
	var logPath string
	defer func() {
		r.forget(p)
		close(p.done)
		r.notify()
	}()

	select {
	case r.sem <- struct{}{}:
	case <-p.cancel:
		r.finish(p.cardID, RunCancelled, 0, "cancelled")
		return
	}
	defer func() { <-r.sem }()

	if p.isCancelled() {
		r.finish(p.cardID, RunCancelled, 0, "cancelled")
		return
	}

	current, err := r.store.Card(p.cardID)
	if err != nil {
		return
	}
	card = *current
	board, err := r.store.Load()
	if err != nil {
		r.finish(card.ID, RunFailed, -1, "load board: "+err.Error())
		return
	}

	repo := r.repoRoot(board)
	prepare := r.prepareRun
	if prepare != nil {
		runContext, err = prepare(context.Background(), &card, repo)
		if err != nil {
			r.finish(card.ID, RunFailed, -1, "prepare run: "+err.Error())
			return
		}
	}
	if runContext == nil {
		runContext = &RunContext{Kind: "repo", Path: repo, Base: r.settings.BaseBranch, Label: "repository"}
	}
	runDir := runContext.Path
	if strings.TrimSpace(runDir) == "" {
		runDir = repo
	}

	_, _, _ = r.store.SetRun(card.ID, func(rs *RunState) {
		rs.Log = r.store.LogPath(card.ID)
	})
	var stored *RunContext
	if runContext.Kind != "" && runContext.Kind != "repo" {
		copy := *runContext
		copy.Env = nil
		stored = &copy
	}
	_, _ = r.store.Update(func(b *Board) error {
		idx := cardIndex(b, card.ID)
		if idx >= 0 {
			b.Cards[idx].Context = stored
			b.Cards[idx].Worktree = nil
			b.Cards[idx].UpdatedAt = nowRFC3339()
		}
		return nil
	})

	logPath = r.store.LogPath(card.ID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		r.finish(card.ID, RunFailed, -1, "create log dir: "+err.Error())
		return
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		r.finish(card.ID, RunFailed, -1, "open log: "+err.Error())
		return
	}
	defer logFile.Close()

	args := []string{"run", "--auto"}
	args = append(args, r.settings.AgentArgs...)
	args = append(args, buildPrompt(card, repo, runContext))

	var stdinR, stdinW *os.File
	if r.settings.AutoAnswer {
		var pipeErr error
		stdinR, stdinW, pipeErr = os.Pipe()
		if pipeErr != nil {
			r.finish(card.ID, RunFailed, -1, "create stdin pipe: "+pipeErr.Error())
			return
		}
		defer stdinR.Close()
		defer stdinW.Close()
	}

	cmd := exec.Command(r.settings.PeggBinary, args...)
	cmd.Dir = runDir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if stdinR != nil {
		cmd.Stdin = stdinR
	}
	configureProcessGroup(cmd)
	env := os.Environ()
	env = setEnv(env, "PEGG_KANBAN_SERVER", r.currentServerURL())
	env = setEnv(env, "PEGG_KANBAN_CARD", card.ID)
	env = setEnv(env, "PEGG_KANBAN_BOARD", r.store.Path())
	if r.settings.APIToken != "" {
		env = setEnv(env, "PEGG_KANBAN_TOKEN", r.settings.APIToken)
	}
	for key, value := range runContext.Env {
		env = setEnv(env, key, value)
	}
	cmd.Env = env

	p.mu.Lock()
	if p.cancelled {
		p.mu.Unlock()
		r.finish(card.ID, RunCancelled, 0, "cancelled")
		return
	}
	if err := cmd.Start(); err != nil {
		p.mu.Unlock()
		r.finish(card.ID, RunFailed, -1, "start pegg run: "+err.Error())
		return
	}
	p.cmd = cmd
	p.mu.Unlock()

	if stdinW != nil {
		go r.autoAnswer(stdinW, p.done)
	}

	_, _, _ = r.store.SetRun(card.ID, func(rs *RunState) {
		rs.Status = RunRunning
		rs.StartedAt = nowRFC3339()
		rs.FinishedAt = ""
		rs.ExitCode = 0
		rs.Error = ""
		rs.PID = cmd.Process.Pid
		rs.Log = logPath
	})
	r.notify()

	waitErr := cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	if p.isCancelled() {
		r.finish(card.ID, RunCancelled, exitCode, "cancelled")
		return
	}
	if exitCode != 0 {
		r.finish(card.ID, RunFailed, exitCode, lastLines(logPath, 20))
		return
	}

	if r.settings.AutoMoveOnSuccess {
		_, _, _ = r.store.Move(card.ID, r.settings.ReviewColumn, -1)
	}
	r.finish(card.ID, RunSucceeded, 0, "")
	if latest, err := r.store.Card(card.ID); err == nil {
		card = *latest
	}
	r.runSuccessHooks(card, runContext)
}

func (r *Runner) finish(cardID, status string, exitCode int, errText string) {
	errText = redactSecrets(errText)
	_, _, _ = r.store.SetRun(cardID, func(rs *RunState) {
		rs.Status = status
		rs.FinishedAt = nowRFC3339()
		rs.ExitCode = exitCode
		rs.Error = errText
	})
	r.notify()
}

func (r *Runner) autoAnswer(w *os.File, done <-chan struct{}) {
	defer w.Close()
	value := strings.TrimSpace(r.settings.AutoAnswerValue)
	if value == "" {
		value = "1"
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if _, err := w.WriteString(value + "\n"); err != nil {
				return
			}
		}
	}
}

func (r *Runner) runSuccessHooks(card Card, runContext *RunContext) {
	r.mu.Lock()
	fn := r.onSuccess
	r.mu.Unlock()
	if fn == nil {
		return
	}
	go fn(&card, runContext)
}

func (r *Runner) forget(p *runProc) {
	r.mu.Lock()
	delete(r.running, p.cardID)
	r.mu.Unlock()
}

func (r *Runner) triggerColumn(board *Board) string {
	if _, ok := findColumn(board, r.settings.TriggerColumn); ok {
		return r.settings.TriggerColumn
	}
	for _, col := range board.Columns {
		if col.Trigger {
			return col.ID
		}
	}
	return "in_progress"
}

func (r *Runner) repoRoot(board *Board) string {
	if board.Repo != "" {
		if fi, err := os.Stat(board.Repo); err == nil && fi.IsDir() {
			if root, err := gitRepoRoot(board.Repo); err == nil {
				return root
			}
		}
	}
	if root, err := gitRepoRoot(r.settings.Workdir); err == nil {
		return root
	}
	return r.settings.Workdir
}

func (r *Runner) currentServerURL() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.serverURL
}

func buildPrompt(card Card, repo string, runContext *RunContext) string {
	priority := card.Priority
	if priority == "" {
		priority = "medium"
	}
	labels := strings.Join(card.Labels, ", ")
	if labels == "" {
		labels = "(none)"
	}
	environmentBlock := ""
	environmentInstruction := "Work inside the repository. Do not modify unrelated files."
	if runContext != nil && strings.TrimSpace(runContext.Kind) != "" && strings.TrimSpace(runContext.Kind) != "repo" {
		var lines []string
		lines = append(lines, "Environment: "+runContext.Kind)
		if label := strings.TrimSpace(runContext.Label); label != "" {
			lines = append(lines, "Label: "+label)
		}
		if path := strings.TrimSpace(runContext.Path); path != "" {
			lines = append(lines, "Path: "+path)
		}
		if branch := strings.TrimSpace(runContext.Branch); branch != "" {
			lines = append(lines, "Branch: "+branch)
		}
		environmentBlock = strings.Join(lines, "\n")
		environmentInstruction = "Work only inside this environment. Do not modify the main checkout."
	}
	issueBlock := ""
	issueNumber := ""
	if card.Issue != nil {
		if card.Issue.Number > 0 {
			issueNumber = fmt.Sprintf("%d", card.Issue.Number)
		}
		label := "issue"
		if provider := strings.TrimSpace(card.Issue.Provider); provider != "" {
			label = provider + " issue"
		}
		ref := strings.TrimSpace(card.Issue.Key)
		if ref == "" && card.Issue.Number > 0 {
			ref = fmt.Sprintf("%s#%d", card.Issue.Repo, card.Issue.Number)
		}
		if ref == "" {
			ref = card.Issue.URL
		}
		issueBlock = fmt.Sprintf("%s: %s %s", label, strings.TrimSpace(ref), card.Issue.URL)
		issueBlock = strings.TrimSpace(issueBlock)
	}
	replacer := strings.NewReplacer(
		"{{title}}", card.Title,
		"{{id}}", card.ID,
		"{{priority}}", priority,
		"{{labels}}", labels,
		"{{column}}", card.Column,
		"{{body}}", card.Body,
		"{{repo}}", repo,
		"{{environment_block}}", environmentBlock,
		"{{environment_instruction}}", environmentInstruction,
		"{{issue_block}}", issueBlock,
		"{{issue_number}}", issueNumber,
	)
	return replacer.Replace(promptTemplate)
}

func lastLines(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
