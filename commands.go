package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

func commandSpecs() []peggplugin.CommandSpec {
	return []peggplugin.CommandSpec{{
		Use:   "kanban",
		Short: "Local Kanban board with agent runs and optional run environments",
		Long:  "Start the Kanban web server, add and move cards, run the agent, inspect logs, and sync integration issues.",
		Children: []peggplugin.CommandSpec{
			{Use: "start", Short: "Start the detached Kanban web server", Flags: serverFlags()},
			{Use: "stop", Short: "Stop the detached Kanban web server", Flags: []peggplugin.FlagSpec{workdirFlag()}},
			{Use: "status", Short: "Show server and board status", Flags: []peggplugin.FlagSpec{workdirFlag()}},
			{Use: "add <title>", Short: "Add a card", MinArgs: 1, MaxArgs: 1, Flags: []peggplugin.FlagSpec{
				workdirFlag(),
				{Name: "column", Usage: "Destination column (default backlog)"},
				{Name: "body", Usage: "Card description"},
				{Name: "priority", Usage: "low, medium, high, urgent"},
				{Name: "labels", Usage: "Comma-separated labels"},
				{Name: "assignee", Usage: "Assignee"},
			}},
			{Use: "list", Short: "List cards", Flags: []peggplugin.FlagSpec{
				workdirFlag(),
				{Name: "column", Usage: "Filter by column"},
				{Name: "all", Usage: "Include all columns", Bool: true},
			}},
			{Use: "move <id> <column>", Short: "Move a card to a column", MinArgs: 2, MaxArgs: 2, Flags: []peggplugin.FlagSpec{workdirFlag()}},
			{Use: "sync", Short: "Sync issues from integration hooks onto the board", Flags: []peggplugin.FlagSpec{
				workdirFlag(),
				{Name: "provider", Usage: "Provider id, or all (default)"},
				{Name: "target", Usage: "Provider target, repo, or project"},
				{Name: "query", Usage: "Provider-specific query, such as JQL"},
				{Name: "labels", Usage: "Comma-separated labels"},
				{Name: "state", Usage: "Provider issue state"},
				{Name: "limit", Usage: "Maximum issues"},
			}},
			{Use: "config", Short: "Show sanitized configuration", Flags: []peggplugin.FlagSpec{workdirFlag()}},
			{Use: "logs <id>", Short: "Print a card's agent log", MinArgs: 1, MaxArgs: 1, Flags: []peggplugin.FlagSpec{
				workdirFlag(),
				{Name: "tail", Usage: "Trailing lines (default 200)"},
			}},
			{Use: "open", Short: "Open the board in a browser, starting the server if needed", Flags: serverFlags()},
		},
	}}
}

func serverFlags() []peggplugin.FlagSpec {
	return []peggplugin.FlagSpec{
		{Name: "host", Usage: "Bind host"},
		{Name: "port", Usage: "Bind port"},
		workdirFlag(),
		{Name: "open", Usage: "Open the browser", Bool: true},
	}
}

func workdirFlag() peggplugin.FlagSpec {
	return peggplugin.FlagSpec{Name: "workdir", Usage: "Workspace directory"}
}

func (p *Plugin) runCommand(req peggplugin.CommandRequest) (string, error) {
	if len(req.Path) == 0 || req.Path[0] != "kanban" {
		return "", fmt.Errorf("unknown kanban command")
	}
	sub := "start"
	if len(req.Path) > 1 {
		sub = req.Path[1]
	}
	settings := p.currentSettings()
	if err := applyCLIFlags(&settings, req.Flags); err != nil {
		return "", err
	}
	return runKanbanCommand(settings, sub, req.Args, req.Flags)
}

func runKanbanCommand(settings Settings, sub string, args []string, flags map[string]string) (string, error) {
	settings.normalize()
	if settings.PluginPath == "" {
		if path, err := os.Executable(); err == nil {
			settings.PluginPath = path
		}
	}
	switch sub {
	case "", "start":
		open := boolFlag(flags, "open", settings.AutoOpen) && flags["no-open"] != "true"
		url, err := commandStart(settings, open)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Kanban server: %s\nBoard: %s\n", url, settings.boardPath()), nil
	case "stop":
		return commandStop(settings)
	case "status":
		return commandStatus(settings)
	case "open":
		url, err := commandStart(settings, true)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Opened %s\n", url), nil
	case "add":
		return commandAdd(settings, args, flags)
	case "list":
		return commandList(settings, args, flags)
	case "move":
		return commandMove(settings, args)
	case "sync":
		return commandSync(settings, args, flags)
	case "config":
		return commandConfig(settings)
	case "logs":
		return commandLogs(settings, args, flags)
	default:
		return "", fmt.Errorf("unknown kanban command %q", sub)
	}
}

func commandAdd(settings Settings, args []string, flags map[string]string) (string, error) {
	title := strings.TrimSpace(flagValue(flags, "title"))
	if title == "" && len(args) > 0 {
		title = strings.TrimSpace(args[0])
	}
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	if base := runningServerURL(settings); base != "" {
		var card Card
		payload := map[string]any{
			"title":    title,
			"body":     flagValue(flags, "body"),
			"column":   flagValue(flags, "column"),
			"priority": flagValue(flags, "priority"),
			"labels":   splitList(flagValue(flags, "labels")),
			"assignee": flagValue(flags, "assignee"),
		}
		if err := serverRequest(settings, http.MethodPost, base, "/api/cards", payload, &card); err != nil {
			return "", err
		}
		return fmt.Sprintf("Added %s %q to %s\n", card.ID, card.Title, card.Column), nil
	}
	store := NewStore(settings.boardPath())
	board, err := store.Load()
	if err != nil {
		return "", err
	}
	column := flagValue(flags, "column")
	if column == "" {
		column = "backlog"
	}
	card := Card{
		ID:       newCardID(),
		Title:    title,
		Body:     flagValue(flags, "body"),
		Column:   column,
		Priority: flagValue(flags, "priority"),
		Labels:   splitList(flagValue(flags, "labels")),
		Assignee: flagValue(flags, "assignee"),
		Source:   "manual",
		Run:      RunState{Status: RunIdle},
	}
	if _, ok := findColumn(board, column); !ok {
		return "", fmt.Errorf("%w: %s", ErrColumnNotFound, column)
	}
	if _, err := store.Add(card); err != nil {
		return "", err
	}
	created, err := store.Card(card.ID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Added %s %q to %s\n", created.ID, created.Title, created.Column), nil
}

func commandList(settings Settings, args []string, flags map[string]string) (string, error) {
	store := NewStore(settings.boardPath())
	board, err := store.Load()
	if err != nil {
		return "", err
	}
	column := flagValue(flags, "column")
	order := map[string]int{}
	for i, col := range board.Columns {
		order[col.ID] = i
	}
	cards := append([]Card(nil), board.Cards...)
	sort.SliceStable(cards, func(i, j int) bool {
		if order[cards[i].Column] != order[cards[j].Column] {
			return order[cards[i].Column] < order[cards[j].Column]
		}
		return cards[i].Position < cards[j].Position
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-12s %-10s %s\n", "ID", "COLUMN", "RUN", "TITLE")
	count := 0
	for _, card := range cards {
		if column != "" && card.Column != column {
			continue
		}
		fmt.Fprintf(&b, "%-24s %-12s %-10s %s\n", card.ID, card.Column, card.Run.Status, card.Title)
		count++
	}
	if count == 0 {
		b.WriteString("(no cards)\n")
	}
	return b.String(), nil
}

func commandMove(settings Settings, args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: kanban move <id> <column>")
	}
	id, column := args[0], args[1]
	store := NewStore(settings.boardPath())
	board, err := store.Load()
	if err != nil {
		return "", err
	}
	if _, ok := findColumn(board, column); !ok {
		return "", fmt.Errorf("%w: %s", ErrColumnNotFound, column)
	}
	base := runningServerURL(settings)
	if base != "" {
		var card Card
		if err := serverRequest(settings, http.MethodPost, base, "/api/cards/"+id+"/move", map[string]any{
			"column": column, "position": -1,
		}, &card); err != nil {
			return "", err
		}
		return fmt.Sprintf("Moved %s to %s\n", card.ID, card.Column), nil
	}

	_, card, err := store.Move(id, column, -1)
	if err != nil {
		return "", err
	}
	if column == triggerColumnFor(settings, board) && isRunnable(card.Run.Status) {
		if _, err := commandStart(settings, false); err != nil {
			return "", err
		}
		if base := runningServerURL(settings); base != "" {
			if err := serverRequest(settings, http.MethodPost, base, "/api/cards/"+id+"/run", nil, nil); err != nil {
				return "", err
			}
		}
	}
	return fmt.Sprintf("Moved %s to %s\n", card.ID, card.Column), nil
}

func commandSync(settings Settings, args []string, flags map[string]string) (string, error) {
	limit, _ := strconv.Atoi(flagValue(flags, "limit"))
	provider := strings.ToLower(strings.TrimSpace(flagValue(flags, "provider")))
	if provider == "" {
		provider = "all"
	}
	req := SyncRequest{
		Provider: provider,
		Target:   flagValue(flags, "target"),
		Labels:   splitList(flagValue(flags, "labels")),
		State:    flagValue(flags, "state"),
		Limit:    limit,
		Query:    flagValue(flags, "query"),
	}
	if base := runningServerURL(settings); base != "" {
		if provider == "all" {
			var result SyncAllResult
			if err := serverRequest(settings, http.MethodPost, base, "/api/sync", req, &result); err != nil {
				return "", err
			}
			return formatSyncAllResult(result), nil
		}
		var stats SyncStats
		if err := serverRequest(settings, http.MethodPost, base, "/api/sync", req, &stats); err != nil {
			return "", err
		}
		return formatSyncStats(stats) + "\n", nil
	}
	svc, err := NewService(settings)
	if err != nil {
		return "", err
	}
	defer func() { _ = svc.Stop(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if provider == "all" {
		result, err := svc.SyncAllProviders(ctx, req)
		if err != nil {
			return "", err
		}
		return formatSyncAllResult(result), nil
	}
	stats, err := svc.SyncProvider(ctx, req)
	if err != nil {
		return "", err
	}
	return formatSyncStats(stats) + "\n", nil
}

func commandConfig(settings Settings) (string, error) {
	data, err := json.MarshalIndent(settings.sanitized(), "", "  ")
	if err != nil {
		return "", err
	}
	return string(data) + "\n", nil
}

func commandLogs(settings Settings, args []string, flags map[string]string) (string, error) {
	if len(args) < 1 {
		return "", fmt.Errorf("usage: kanban logs <id>")
	}
	store := NewStore(settings.boardPath())
	card, err := store.Card(args[0])
	if err != nil {
		return "", err
	}
	tail := 200
	if value := flagValue(flags, "tail"); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			tail = n
		}
	}
	return lastLines(store.LogPath(card.ID), tail) + "\n", nil
}

func flagValue(flags map[string]string, name string) string {
	if flags == nil {
		return ""
	}
	return strings.TrimSpace(flags[name])
}

func boolFlag(flags map[string]string, name string, fallback bool) bool {
	value, ok := flags[name]
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}
