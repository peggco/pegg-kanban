package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

func (p *Plugin) remoteServer() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("PEGG_KANBAN_SERVER")), "/")
}

func (p *Plugin) executeTool(ctx context.Context, name, raw string) (*peggplugin.ToolResult, error) {
	switch name {
	case "kanban_status":
		return p.toolStatus(ctx)
	case "kanban_list":
		return p.toolList(ctx, raw)
	case "kanban_add":
		return p.toolAdd(ctx, raw)
	case "kanban_update":
		return p.toolUpdate(ctx, raw)
	case "kanban_move":
		return p.toolMove(ctx, raw)
	case "kanban_run":
		return p.toolRun(ctx, raw)
	case "kanban_cancel":
		return p.toolCancel(ctx, raw)
	case "kanban_log":
		return p.toolLog(ctx, raw)
	case "kanban_sync":
		return p.toolSync(ctx, raw)
	case "kanban_config":
		return p.toolConfig(ctx)
	default:
		return nil, fmt.Errorf("unknown kanban tool %q", name)
	}
}

func (p *Plugin) toolStatus(ctx context.Context) (*peggplugin.ToolResult, error) {
	board, err := p.toolsBoard(ctx)
	if err != nil {
		return nil, err
	}
	active := []string{}
	for _, card := range board.Cards {
		if card.Run.Status == RunQueued || card.Run.Status == RunRunning {
			active = append(active, card.ID)
		}
	}
	out := map[string]any{
		"ok":         true,
		"server":     p.remoteServer(),
		"board":      p.currentSettings().boardPath(),
		"name":       board.Name,
		"repo":       board.Repo,
		"columns":    board.Columns,
		"cards":      len(board.Cards),
		"active":     active,
		"updated_at": board.UpdatedAt,
		"sync":       board.Sync,
	}
	if availability := p.providerAvailability(ctx); availability != nil {
		out["providers"] = availability
	}
	return textResult(prettyJSON(out)), nil
}

func (p *Plugin) providerAvailability(ctx context.Context) any {
	if p.remoteServer() != "" {
		data, err := p.apiText(ctx, "/api/config")
		if err != nil {
			return nil
		}
		var config map[string]any
		if err := json.Unmarshal([]byte(data), &config); err != nil {
			return nil
		}
		return config["providers"]
	}
	svc, err := p.localService()
	if err != nil {
		return nil
	}
	providers, err := svc.Providers(ctx)
	if err != nil {
		return nil
	}
	return providers
}

func (p *Plugin) toolList(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		Column string `json:"column"`
		Status string `json:"status"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	board, err := p.toolsBoard(ctx)
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, col := range board.Columns {
		order[col.ID] = i
	}
	var cards []Card
	for _, card := range board.Cards {
		if args.Column != "" && card.Column != args.Column {
			continue
		}
		if args.Status != "" && card.Run.Status != args.Status {
			continue
		}
		cards = append(cards, card)
	}
	sort.SliceStable(cards, func(i, j int) bool {
		if order[cards[i].Column] != order[cards[j].Column] {
			return order[cards[i].Column] < order[cards[j].Column]
		}
		return cards[i].Position < cards[j].Position
	})
	if cards == nil {
		cards = []Card{}
	}
	return textResult(prettyJSON(map[string]any{"count": len(cards), "cards": cards})), nil
}

func (p *Plugin) toolAdd(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		Title         string   `json:"title"`
		Body          string   `json:"body"`
		Column        string   `json:"column"`
		Priority      string   `json:"priority"`
		Labels        []string `json:"labels"`
		Assignee      string   `json:"assignee"`
		IssueProvider string   `json:"issue_provider"`
		IssueRepo     string   `json:"issue_repo"`
		IssueNumber   int      `json:"issue_number"`
		IssueKey      string   `json:"issue_key"`
		IssueURL      string   `json:"issue_url"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	var issue *IssueRef
	if args.IssueNumber > 0 || args.IssueRepo != "" || strings.TrimSpace(args.IssueKey) != "" {
		issue = &IssueRef{
			Provider: args.IssueProvider,
			Repo:     args.IssueRepo,
			Number:   args.IssueNumber,
			Key:      args.IssueKey,
			URL:      args.IssueURL,
		}
	}
	payload := map[string]any{
		"title":    args.Title,
		"body":     args.Body,
		"column":   args.Column,
		"priority": args.Priority,
		"labels":   args.Labels,
		"assignee": args.Assignee,
	}
	if issue != nil {
		payload["issue"] = issue
	}

	if p.remoteServer() != "" {
		var card Card
		if err := p.apiRequest(ctx, http.MethodPost, "/api/cards", payload, &card); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(card)), nil
	}

	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	board, err := svc.store.Load()
	if err != nil {
		return nil, err
	}
	column := args.Column
	if column == "" {
		column = board.Columns[0].ID
	}
	source := "manual"
	if issue != nil && strings.TrimSpace(issue.Provider) != "" {
		source = strings.TrimSpace(issue.Provider)
	}
	card := Card{
		ID:       newCardID(),
		Title:    strings.TrimSpace(args.Title),
		Body:     args.Body,
		Column:   column,
		Priority: args.Priority,
		Labels:   args.Labels,
		Assignee: args.Assignee,
		Source:   source,
		Issue:    issue,
		Run:      RunState{Status: RunIdle},
	}
	if _, err := svc.store.Add(card); err != nil {
		return nil, err
	}
	created, err := svc.store.Card(card.ID)
	if err != nil {
		return nil, err
	}
	return textResult(prettyJSON(created)), nil
}

type cardPatch struct {
	Title    *string   `json:"title,omitempty"`
	Body     *string   `json:"body,omitempty"`
	Priority *string   `json:"priority,omitempty"`
	Labels   *[]string `json:"labels,omitempty"`
	Assignee *string   `json:"assignee,omitempty"`
	Column   *string   `json:"column,omitempty"`
	Position *int      `json:"position,omitempty"`
}

func (p *Plugin) toolUpdate(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		ID       string    `json:"id"`
		Title    *string   `json:"title"`
		Body     *string   `json:"body"`
		Priority *string   `json:"priority"`
		Labels   *[]string `json:"labels"`
		Assignee *string   `json:"assignee"`
		Column   *string   `json:"column"`
		Position *int      `json:"position"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	patch := cardPatch{
		Title: args.Title, Body: args.Body, Priority: args.Priority,
		Labels: args.Labels, Assignee: args.Assignee, Column: args.Column, Position: args.Position,
	}
	if p.remoteServer() != "" {
		var card Card
		if err := p.apiRequest(ctx, http.MethodPatch, "/api/cards/"+args.ID, patch, &card); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(card)), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	if err := applyPatch(svc.store, args.ID, patch); err != nil {
		return nil, err
	}
	if patch.Column != nil {
		if board, err := svc.store.Load(); err == nil && *patch.Column == svc.triggerColumn(board) {
			if card, err := svc.store.Card(args.ID); err == nil && isRunnable(card.Run.Status) {
				if err := svc.runner.Trigger(args.ID); err != nil {
					return nil, err
				}
			}
		}
	}
	card, err := svc.store.Card(args.ID)
	if err != nil {
		return nil, err
	}
	return textResult(prettyJSON(card)), nil
}

func applyPatch(store *Store, id string, patch cardPatch) error {
	_, err := store.Update(func(b *Board) error {
		idx := cardIndex(b, id)
		if idx < 0 {
			return ErrCardNotFound
		}
		card := &b.Cards[idx]
		if patch.Title != nil {
			if strings.TrimSpace(*patch.Title) == "" {
				return fmt.Errorf("title cannot be empty")
			}
			card.Title = strings.TrimSpace(*patch.Title)
		}
		if patch.Body != nil {
			card.Body = *patch.Body
		}
		if patch.Priority != nil {
			card.Priority = *patch.Priority
		}
		if patch.Labels != nil {
			card.Labels = *patch.Labels
		}
		if patch.Assignee != nil {
			card.Assignee = *patch.Assignee
		}
		card.UpdatedAt = nowRFC3339()
		return nil
	})
	if err != nil {
		return err
	}
	if patch.Column != nil {
		position := -1
		if patch.Position != nil {
			position = *patch.Position
		}
		if _, _, err := store.Move(id, *patch.Column, position); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) toolMove(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		ID       string `json:"id"`
		Column   string `json:"column"`
		Position *int   `json:"position"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ID == "" || args.Column == "" {
		return nil, fmt.Errorf("id and column are required")
	}
	if p.remoteServer() != "" {
		position := -1
		if args.Position != nil {
			position = *args.Position
		}
		var card Card
		payload := map[string]any{"column": args.Column, "position": position}
		if err := p.apiRequest(ctx, http.MethodPost, "/api/cards/"+args.ID+"/move", payload, &card); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(card)), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	position := -1
	if args.Position != nil {
		position = *args.Position
	}
	board, card, err := svc.store.Move(args.ID, args.Column, position)
	if err != nil {
		return nil, err
	}
	if args.Column == svc.triggerColumn(board) && isRunnable(card.Run.Status) {
		if err := svc.runner.Trigger(args.ID); err != nil {
			return nil, err
		}
		card, _ = svc.store.Card(args.ID)
	}
	return textResult(prettyJSON(card)), nil
}

func (p *Plugin) toolRun(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if p.remoteServer() != "" {
		var card Card
		if err := p.apiRequest(ctx, http.MethodPost, "/api/cards/"+args.ID+"/run", nil, &card); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(card)), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	if err := svc.runner.Trigger(args.ID); err != nil {
		return nil, err
	}
	card, err := svc.store.Card(args.ID)
	if err != nil {
		return nil, err
	}
	return textResult(prettyJSON(card)), nil
}

func (p *Plugin) toolCancel(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		ID string `json:"id"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if p.remoteServer() != "" {
		var card Card
		if err := p.apiRequest(ctx, http.MethodPost, "/api/cards/"+args.ID+"/cancel", nil, &card); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(card)), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	if err := svc.runner.Cancel(args.ID); err != nil {
		return nil, err
	}
	card, err := svc.store.Card(args.ID)
	if err != nil {
		return nil, err
	}
	return textResult(prettyJSON(card)), nil
}

func (p *Plugin) toolLog(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		ID   string `json:"id"`
		Tail int    `json:"tail"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	if args.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if args.Tail <= 0 {
		args.Tail = 200
	}
	if p.remoteServer() != "" {
		data, err := p.apiText(ctx, "/api/cards/"+args.ID+"/log?tail="+fmt.Sprintf("%d", args.Tail))
		if err != nil {
			return nil, err
		}
		return textResult(data), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	card, err := svc.store.Card(args.ID)
	if err != nil {
		return nil, err
	}
	return textResult(lastLines(svc.store.LogPath(card.ID), args.Tail)), nil
}

func (p *Plugin) toolSync(ctx context.Context, raw string) (*peggplugin.ToolResult, error) {
	var args struct {
		Provider string   `json:"provider"`
		Target   string   `json:"target"`
		Labels   []string `json:"labels"`
		State    string   `json:"state"`
		Limit    int      `json:"limit"`
		Query    string   `json:"query"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	provider := strings.ToLower(strings.TrimSpace(args.Provider))
	if provider == "" {
		provider = "all"
	}
	req := SyncRequest{
		Provider: provider,
		Target:   args.Target,
		Labels:   args.Labels,
		State:    args.State,
		Limit:    args.Limit,
		Query:    args.Query,
	}
	if p.remoteServer() != "" {
		if provider != "all" {
			var stats SyncStats
			if err := p.apiRequest(ctx, http.MethodPost, "/api/sync", req, &stats); err != nil {
				return nil, err
			}
			return textResult(prettyJSON(stats)), nil
		}
		var result SyncAllResult
		if err := p.apiRequest(ctx, http.MethodPost, "/api/sync", req, &result); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(result)), nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	if provider != "all" {
		stats, err := svc.SyncProvider(ctx, req)
		if err != nil {
			return nil, err
		}
		return textResult(prettyJSON(stats)), nil
	}
	result, err := svc.SyncAllProviders(ctx, req)
	if err != nil {
		return nil, err
	}
	return textResult(prettyJSON(result)), nil
}

func (p *Plugin) toolConfig(ctx context.Context) (*peggplugin.ToolResult, error) {
	if p.remoteServer() != "" {
		data, err := p.apiText(ctx, "/api/config")
		if err != nil {
			return nil, err
		}
		var out any
		if err := json.Unmarshal([]byte(data), &out); err != nil {
			return nil, err
		}
		return textResult(prettyJSON(out)), nil
	}
	out := p.currentSettings().sanitized()
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	providers, err := svc.Providers(ctx)
	if err != nil {
		return nil, err
	}
	out["providers"] = providers
	return textResult(prettyJSON(out)), nil
}

func (p *Plugin) toolsBoard(ctx context.Context) (*Board, error) {
	if p.remoteServer() != "" {
		var board Board
		if err := p.apiRequest(ctx, http.MethodGet, "/api/board", nil, &board); err != nil {
			return nil, err
		}
		return &board, nil
	}
	svc, err := p.localService()
	if err != nil {
		return nil, err
	}
	return svc.store.Load()
}

func (p *Plugin) apiRequest(ctx context.Context, method, path string, body, out any) error {
	data, err := p.apiCall(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode server response: %w", err)
		}
	}
	return nil
}

func (p *Plugin) apiText(ctx context.Context, path string) (string, error) {
	data, err := p.apiCall(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *Plugin) apiCall(ctx context.Context, method, path string, body any) ([]byte, error) {
	base := p.remoteServer()
	if base == "" {
		return nil, fmt.Errorf("PEGG_KANBAN_SERVER is not set")
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := os.Getenv("PEGG_KANBAN_TOKEN"); token != "" {
		req.Header.Set("X-PegG-Token", token)
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kanban server request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &apiErr)
		msg := strings.TrimSpace(apiErr.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("kanban server %d: %s", resp.StatusCode, redactSecrets(msg))
	}
	return data, nil
}

func decodeArgs(raw string, out any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func textResult(text string) *peggplugin.ToolResult {
	return &peggplugin.ToolResult{Output: text}
}

func prettyJSON(value any) string {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func isRunnable(status string) bool {
	switch status {
	case "", RunIdle, RunFailed, RunCancelled, RunSucceeded:
		return true
	default:
		return false
	}
}

func repoFromBoardPath(boardPath string) string {
	root := filepath.Dir(filepath.Dir(filepath.Dir(boardPath)))
	if root == "" || root == "." {
		return ""
	}
	if gitRoot, err := gitRepoRoot(root); err == nil {
		return gitRoot
	}
	return ""
}
