package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

const kanbanTUIPageID = "kanban"

func (p *Plugin) kanbanTUIPage() (peggplugin.TUIPage, error) {
	settings := p.currentSettings()
	svc, err := p.localService()
	if err != nil {
		return peggplugin.TUIPage{}, err
	}
	board, err := svc.store.Load()
	if err != nil {
		return peggplugin.TUIPage{}, err
	}
	providers := p.cachedProviders()
	runningURL := tuiServerURL(settings)

	serverStatus := "stopped"
	serverBadge := "down"
	if runningURL != "" {
		serverStatus = "running"
		serverBadge = "up"
	}

	cardParts := make([]string, 0, len(board.Columns))
	for _, column := range board.Columns {
		cardParts = append(cardParts, fmt.Sprintf("%s=%d", column.ID, countColumn(board, column.ID)))
	}
	cardsSummary := strings.Join(cardParts, " ")
	if cardsSummary == "" {
		cardsSummary = "no columns"
	}

	activeRuns := 0
	for _, card := range board.Cards {
		if card.Run.Status == RunQueued || card.Run.Status == RunRunning {
			activeRuns++
		}
	}

	metrics := []peggplugin.TUIMetric{
		{Label: "Cards", Value: strconv.Itoa(len(board.Cards))},
		{Label: "Running", Value: strconv.Itoa(activeRuns)},
		{Label: "Review", Value: strconv.Itoa(countColumn(board, settings.ReviewColumn))},
		{Label: "Done", Value: strconv.Itoa(countColumn(board, "done"))},
	}
	providerItems := []peggplugin.TUIItem{}
	for _, info := range providers {
		badge := "disabled"
		detail := strings.TrimSpace(info.Detail)
		if info.Available {
			badge = "ready"
		}
		if detail == "" {
			if info.Available {
				detail = "available"
			} else {
				detail = "unavailable"
			}
		}
		if info.Target != "" {
			detail += " · " + info.Target
		}
		if info.SyncMinutes > 0 {
			detail += fmt.Sprintf(" · every %dm", info.SyncMinutes)
		}
		metrics = append(metrics, peggplugin.TUIMetric{Label: info.Label, Value: badge, Detail: detail})
		providerItems = append(providerItems, peggplugin.TUIItem{Title: info.Label, Detail: detail, Badge: badge})
	}
	if len(providerItems) == 0 {
		providerItems = append(providerItems, peggplugin.TUIItem{Title: "No providers", Detail: "Install or enable an integration plugin."})
	}
	syncOptions := []string{"all"}
	for _, info := range providers {
		syncOptions = append(syncOptions, info.ID)
	}

	return peggplugin.TUIPage{
		ID:          kanbanTUIPageID,
		Title:       "Kanban",
		Description: "Local board, isolated agent runs, and optional issue sync through integration hooks.",
		Layout:      "board",
		Metrics:     metrics,
		Sections: []peggplugin.TUISection{
			{
				Title: "Board",
				Kind:  "board",
				Items: kanbanCardItems(board),
			},
			{
				Title: "Server",
				Kind:  "list",
				Items: []peggplugin.TUIItem{
					{Title: "Status", Detail: serverStatus, Badge: serverBadge},
					{Title: "URL", Detail: tuiURLDetail(runningURL)},
					{Title: "Board", Detail: settings.boardPath()},
					{Title: "Columns", Detail: cardsSummary},
				},
			},
			{
				Title: "Providers",
				Kind:  "list",
				Items: providerItems,
				Note:  "Provider setup lives in the integration plugins.",
			},
			{
				Title: "Runner",
				Kind:  "list",
				Items: []peggplugin.TUIItem{
					kanbanEditableItem("Max parallel", strconv.Itoa(settings.MaxParallel), settings, "max_parallel"),
					{Title: "Active runs", Detail: strconv.Itoa(activeRuns)},
					kanbanEditableItem("Trigger column", settings.TriggerColumn, settings, "trigger_column"),
					kanbanEditableItem("Review column", settings.ReviewColumn, settings, "review_column"),
				},
			},
		},
		Settings:   kanbanSettingsFields(settings),
		SaveAction: "settings.save",
		Actions: []peggplugin.TUIAction{
			{
				ID:          "settings.save",
				Label:       "Save settings",
				Description: "Update provider, runner, and sync options.",
				Fields:      kanbanSettingsFields(settings),
			},
			{ID: "start", Label: "Start server", Description: "Start the detached Kanban web server."},
			{ID: "open", Label: "Open board", Description: "Start the server if needed and open the browser."},
			{ID: "stop", Label: "Stop server", Description: "Stop the detached Kanban web server."},
			{ID: "sync", Label: "Sync issues", Description: "Import and update integration issues.", Fields: []peggplugin.TUIField{
				{ID: "provider", Label: "Provider", Value: "all", Options: syncOptions},
				{ID: "target", Label: "Target", Placeholder: "repository, project, or tracker target"},
				{ID: "query", Label: "Query", Placeholder: "provider-specific override"},
				{ID: "labels", Label: "Labels", Placeholder: "bug, ui"},
				{ID: "state", Label: "State", Placeholder: "open / opened"},
				{ID: "limit", Label: "Issue limit", Placeholder: "50"},
			}},
			{
				ID:          "card.add",
				Label:       "Add card",
				Description: "Create a card on the board.",
				Fields: []peggplugin.TUIField{
					{ID: "title", Label: "Title", Required: true},
					{ID: "body", Label: "Description"},
					{ID: "column", Label: "Column", Value: "backlog"},
					{ID: "priority", Label: "Priority", Options: []string{"low", "medium", "high", "urgent"}},
					{ID: "labels", Label: "Labels", Placeholder: "bug, ui"},
					{ID: "assignee", Label: "Assignee"},
				},
			},
			{ID: "card.run", Label: "Run card", Description: "Queue a card for the agent.", Fields: []peggplugin.TUIField{
				{ID: "id", Label: "Card ID", Required: true},
			}},
			{ID: "card.delete", Label: "Delete card", Description: "Remove a card from the board.", Fields: []peggplugin.TUIField{
				{ID: "id", Label: "Card ID", Required: true},
			}},
			{ID: "refresh", Label: "Refresh", Description: "Refresh this page."},
		},
	}, nil
}

func kanbanCardItems(board *Board) []peggplugin.TUIItem {
	items := make([]peggplugin.TUIItem, 0, len(board.Cards))
	for i := range board.Cards {
		card := board.Cards[i]
		detailParts := []string{}
		if card.Priority != "" {
			detailParts = append(detailParts, card.Priority)
		}
		if len(card.Labels) > 0 {
			detailParts = append(detailParts, strings.Join(card.Labels, ", "))
		}
		badge := card.Run.Status
		if badge == "" {
			badge = RunIdle
		}
		meta := map[string]string{
			"runAction":    "card.run",
			"deleteAction": "card.delete",
		}
		if card.Context != nil {
			if card.Context.Branch != "" {
				meta["branch"] = card.Context.Branch
			}
			if card.Context.Kind != "" {
				meta["environment"] = card.Context.Kind
			}
		}
		if card.Issue != nil {
			label := ""
			if key := strings.TrimSpace(card.Issue.Key); key != "" {
				label = key
			} else if card.Issue.Number > 0 {
				if card.Issue.Repo != "" {
					label = fmt.Sprintf("%s#%d", card.Issue.Repo, card.Issue.Number)
				} else {
					label = fmt.Sprintf("#%d", card.Issue.Number)
				}
			}
			if label != "" {
				if card.Issue.Provider != "" {
					label = card.Issue.Provider + " " + label
				}
				meta["issue"] = label
			}
			if card.Issue.URL != "" {
				meta["issue_url"] = card.Issue.URL
			}
		}
		items = append(items, peggplugin.TUIItem{
			ID:     card.ID,
			Title:  card.Title,
			Detail: strings.Join(detailParts, " · "),
			Badge:  badge,
			Column: card.Column,
			Meta:   meta,
			Fields: []peggplugin.TUIField{
				{ID: "title", Label: "Title", Value: card.Title},
				{ID: "body", Label: "Description", Value: card.Body},
				{ID: "column", Label: "Column", Value: card.Column},
				{ID: "priority", Label: "Priority", Value: card.Priority, Options: []string{"", "low", "medium", "high", "urgent"}},
				{ID: "labels", Label: "Labels", Value: strings.Join(card.Labels, ", ")},
				{ID: "assignee", Label: "Assignee", Value: card.Assignee},
			},
			Action: "card.update",
		})
	}
	return items
}

func kanbanSettingsFields(settings Settings) []peggplugin.TUIField {
	boolOptions := []string{"true", "false"}
	return []peggplugin.TUIField{
		{ID: "sync_column", Label: "Import column", Value: settings.SyncColumn},
		{ID: "sync_minutes", Label: "Sync interval (minutes)", Value: strconv.Itoa(settings.SyncMinutes)},
		{ID: "sync_update_existing", Label: "Update existing issues", Value: strconv.FormatBool(settings.SyncUpdateExisting), Options: boolOptions},
		{ID: "sync_labels", Label: "Sync labels", Value: strings.Join(settings.SyncLabels, ", ")},
		{ID: "sync_limit", Label: "Issue limit", Value: strconv.Itoa(settings.SyncLimit)},
		{ID: "sync_state", Label: "Issue state", Value: settings.SyncState},
		{ID: "feedback", Label: "Send feedback on success", Value: strconv.FormatBool(settings.Feedback), Options: boolOptions},
		{ID: "create_change_request", Label: "Create change requests automatically", Value: strconv.FormatBool(settings.CreateChangeRequest), Options: boolOptions},
		{ID: "transition_on_success", Label: "Transition on success", Value: settings.TransitionOnSuccess},
		{ID: "hooks_timeout_seconds", Label: "Hooks timeout (seconds)", Value: strconv.Itoa(settings.HooksTimeout)},
		{ID: "max_parallel", Label: "Max parallel runs", Value: strconv.Itoa(settings.MaxParallel)},
		{ID: "trigger_column", Label: "Trigger column", Value: settings.TriggerColumn},
		{ID: "review_column", Label: "Review column", Value: settings.ReviewColumn},
		{ID: "auto_move_on_success", Label: "Move on success", Value: strconv.FormatBool(settings.AutoMoveOnSuccess), Options: boolOptions},
		{ID: "base_branch", Label: "Base branch", Value: settings.BaseBranch},
		{ID: "pegg_binary", Label: "Pegg binary", Value: settings.PeggBinary},
		{ID: "agent_args", Label: "Agent args", Value: strings.Join(settings.AgentArgs, ", "), Placeholder: "--model, x"},
	}
}

func kanbanEditableItem(title, detail string, settings Settings, fieldID string) peggplugin.TUIItem {
	item := peggplugin.TUIItem{Title: title, Detail: detail, Action: "settings.save"}
	for _, field := range kanbanSettingsFields(settings) {
		if field.ID == fieldID {
			item.Fields = []peggplugin.TUIField{field}
			break
		}
	}
	return item
}

func (p *Plugin) executeTUIAction(req peggplugin.TUIActionRequest) (*peggplugin.TUIActionResult, error) {
	if req.PageID != "" && req.PageID != kanbanTUIPageID {
		return nil, fmt.Errorf("unknown kanban TUI page %q", req.PageID)
	}
	settings := p.currentSettings()
	var output string
	var err error

	switch strings.TrimSpace(req.ActionID) {
	case "settings.save":
		output, err = p.tuiSaveSettings(req.Values)
	case "card.add":
		output, err = p.tuiAddCard(req.Values)
	case "card.update":
		output, err = p.tuiUpdateCard(req.Values)
	case "card.delete":
		output, err = p.tuiDeleteCard(req.Values)
	case "start":
		var url string
		url, err = p.startServer(false)
		if err == nil {
			output = fmt.Sprintf("Kanban server: %s", url)
		}
	case "open":
		var url string
		url, err = p.startServer(true)
		if err == nil {
			output = fmt.Sprintf("Opened %s", url)
		}
	case "stop":
		output, err = p.stopServer()
	case "sync":
		opts := tuiSyncOptions{Provider: "all"}
		if req.Values != nil {
			if v := strings.TrimSpace(req.Values["provider"]); v != "" {
				opts.Provider = v
			}
			opts.Target = strings.TrimSpace(req.Values["target"])
			opts.Query = strings.TrimSpace(req.Values["query"])
			opts.Labels = splitList(req.Values["labels"])
			opts.State = strings.TrimSpace(req.Values["state"])
			if n, err := strconv.Atoi(strings.TrimSpace(req.Values["limit"])); err == nil && n > 0 {
				opts.Limit = n
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		output, err = p.tuiSync(ctx, settings, opts)
		if err == nil {
			p.refreshProviderCache(ctx)
		}
	case "refresh":
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p.refreshProviderCache(ctx)
		cancel()
		output = "Refreshed"
	default:
		return nil, fmt.Errorf("unknown kanban action %q", req.ActionID)
	}
	if err != nil {
		return nil, err
	}

	page, err := p.kanbanTUIPage()
	if err != nil {
		return &peggplugin.TUIActionResult{Output: output}, err
	}
	return &peggplugin.TUIActionResult{Output: output, Page: &page}, nil
}

func (p *Plugin) tuiSaveSettings(values map[string]string) (string, error) {
	settings := p.currentSettings()
	if v, ok := values["sync_column"]; ok && strings.TrimSpace(v) != "" {
		settings.SyncColumn = strings.TrimSpace(v)
	}
	if v, ok := values["sync_minutes"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n >= 0 {
			settings.SyncMinutes = n
		}
	}
	if v, ok := values["sync_update_existing"]; ok {
		settings.SyncUpdateExisting = parseBoolValue(v, settings.SyncUpdateExisting)
	}
	if v, ok := values["sync_labels"]; ok {
		settings.SyncLabels = splitList(v)
	}
	if v, ok := values["sync_limit"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			settings.SyncLimit = n
		}
	}
	if v, ok := values["sync_state"]; ok {
		settings.SyncState = strings.TrimSpace(v)
	}
	if v, ok := values["feedback"]; ok {
		settings.Feedback = parseBoolValue(v, settings.Feedback)
	}
	if v, ok := values["create_change_request"]; ok {
		settings.CreateChangeRequest = parseBoolValue(v, settings.CreateChangeRequest)
	}
	if v, ok := values["transition_on_success"]; ok {
		settings.TransitionOnSuccess = strings.TrimSpace(v)
	}
	if v, ok := values["hooks_timeout_seconds"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			settings.HooksTimeout = n
		}
	}
	if v, ok := values["max_parallel"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			settings.MaxParallel = n
		}
	}
	if v, ok := values["trigger_column"]; ok && strings.TrimSpace(v) != "" {
		settings.TriggerColumn = strings.TrimSpace(v)
	}
	if v, ok := values["review_column"]; ok && strings.TrimSpace(v) != "" {
		settings.ReviewColumn = strings.TrimSpace(v)
	}
	if v, ok := values["auto_move_on_success"]; ok {
		settings.AutoMoveOnSuccess = parseBoolValue(v, settings.AutoMoveOnSuccess)
	}
	if v, ok := values["base_branch"]; ok {
		settings.BaseBranch = strings.TrimSpace(v)
	}
	if v, ok := values["pegg_binary"]; ok {
		settings.PeggBinary = strings.TrimSpace(v)
	}
	if v, ok := values["agent_args"]; ok {
		settings.AgentArgs = splitList(v)
	}
	settings.normalize()

	p.mu.Lock()
	p.settings = settings
	p.mu.Unlock()

	path, err := persistKanbanSettings(settings)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Settings saved to %s", path), nil
}

func (p *Plugin) tuiAddCard(values map[string]string) (string, error) {
	svc, err := p.localService()
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(values["title"])
	if title == "" {
		return "", fmt.Errorf("title is required")
	}
	column := strings.TrimSpace(values["column"])
	if column == "" {
		column = "backlog"
	}
	card := Card{
		ID:       newCardID(),
		Title:    title,
		Body:     strings.TrimSpace(values["body"]),
		Column:   column,
		Priority: strings.TrimSpace(values["priority"]),
		Labels:   splitList(values["labels"]),
		Assignee: strings.TrimSpace(values["assignee"]),
		Source:   "manual",
		Run:      RunState{Status: RunIdle},
	}
	if _, err := svc.store.Add(card); err != nil {
		return "", err
	}
	return fmt.Sprintf("Added %q to %s", card.Title, card.Column), nil
}

func (p *Plugin) tuiUpdateCard(values map[string]string) (string, error) {
	svc, err := p.localService()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(values["id"])
	if id == "" {
		return "", fmt.Errorf("card id is required")
	}
	column := strings.TrimSpace(values["column"])
	_, err = svc.store.Update(func(b *Board) error {
		idx := cardIndex(b, id)
		if idx < 0 {
			return ErrCardNotFound
		}
		card := &b.Cards[idx]
		if v, ok := values["title"]; ok && strings.TrimSpace(v) != "" {
			card.Title = strings.TrimSpace(v)
		}
		if v, ok := values["body"]; ok {
			card.Body = v
		}
		if v, ok := values["priority"]; ok {
			card.Priority = strings.TrimSpace(v)
		}
		if v, ok := values["labels"]; ok {
			card.Labels = splitList(v)
		}
		if v, ok := values["assignee"]; ok {
			card.Assignee = strings.TrimSpace(v)
		}
		card.UpdatedAt = nowRFC3339()
		return nil
	})
	if err != nil {
		return "", err
	}
	if column != "" {
		if _, _, err := svc.store.Move(id, column, -1); err != nil {
			return "", err
		}
	}
	return "Card updated", nil
}

func (p *Plugin) tuiRunCard(values map[string]string) (string, error) {
	svc, err := p.localService()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(values["id"])
	if id == "" {
		return "", fmt.Errorf("card id is required")
	}
	card, err := svc.store.Card(id)
	if err != nil {
		return "", err
	}
	board, err := svc.store.Load()
	if err != nil {
		return "", err
	}
	trigger := svc.runner.triggerColumn(board)
	if card.Column != trigger {
		if _, _, err := svc.store.Move(id, trigger, -1); err != nil {
			return "", err
		}
	}
	if err := svc.runner.Trigger(id); err != nil {
		return "", err
	}
	return "Card queued", nil
}

func (p *Plugin) tuiDeleteCard(values map[string]string) (string, error) {
	svc, err := p.localService()
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(values["id"])
	if id == "" {
		return "", fmt.Errorf("card id is required")
	}
	if _, err := svc.store.Delete(id); err != nil {
		return "", err
	}
	return "Card deleted", nil
}

func parseBoolValue(value string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "on":
		return true
	case "false", "no", "0", "off":
		return false
	default:
		return fallback
	}
}

func kanbanConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("~", ".pegg", "pegg.json")
	}
	return filepath.Join(home, ".pegg", "pegg.json")
}

func persistKanbanSettings(settings Settings) (string, error) {
	path := kanbanConfigPath()
	root := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	} else if len(data) > 0 {
		if err := json.Unmarshal(data, &root); err != nil {
			return "", fmt.Errorf("parse %s: %w", path, err)
		}
	}

	plugins, ok := root["plugins"].(map[string]any)
	if !ok {
		plugins = map[string]any{}
		root["plugins"] = plugins
	}
	options, ok := plugins["options"].(map[string]any)
	if !ok {
		options = map[string]any{}
		plugins["options"] = options
	}
	key := "kanban"
	if _, ok := options["kanban"]; !ok {
		if _, ok := options["pegg-kanban"]; ok {
			key = "pegg-kanban"
		}
	}
	entry, ok := options[key].(map[string]any)
	if !ok {
		entry = map[string]any{}
	}
	entry["sync_column"] = settings.SyncColumn
	entry["sync_minutes"] = settings.SyncMinutes
	entry["sync_update_existing"] = settings.SyncUpdateExisting
	entry["sync_labels"] = settings.SyncLabels
	entry["sync_limit"] = settings.SyncLimit
	entry["sync_state"] = settings.SyncState
	entry["feedback"] = settings.Feedback
	entry["create_change_request"] = settings.CreateChangeRequest
	entry["transition_on_success"] = settings.TransitionOnSuccess
	entry["hooks_timeout_seconds"] = settings.HooksTimeout
	entry["max_parallel"] = settings.MaxParallel
	entry["trigger_column"] = settings.TriggerColumn
	entry["review_column"] = settings.ReviewColumn
	entry["auto_move_on_success"] = settings.AutoMoveOnSuccess
	entry["base_branch"] = settings.BaseBranch
	entry["pegg_binary"] = settings.PeggBinary
	entry["agent_args"] = settings.AgentArgs
	options[key] = entry

	encoded, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", path, err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".pegg-*.json")
	if err != nil {
		return "", fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(encoded); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("set config permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	return path, nil
}

type tuiSyncOptions struct {
	Provider string
	Target   string
	Query    string
	Labels   []string
	State    string
	Limit    int
}

func (p *Plugin) tuiSync(ctx context.Context, settings Settings, opts tuiSyncOptions) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(opts.Provider))
	if provider == "" {
		provider = "all"
	}
	req := SyncRequest{
		Provider: provider,
		Target:   opts.Target,
		Query:    opts.Query,
		Labels:   opts.Labels,
		State:    opts.State,
		Limit:    opts.Limit,
	}
	if base := runningServerURL(settings); base != "" {
		if provider == "all" {
			var result SyncAllResult
			if err := serverRequestContext(ctx, settings, http.MethodPost, base, "/api/sync", req, &result); err != nil {
				return "", err
			}
			return formatSyncAllResult(result), nil
		}
		var stats SyncStats
		if err := serverRequestContext(ctx, settings, http.MethodPost, base, "/api/sync", req, &stats); err != nil {
			return "", err
		}
		return formatSyncStats(stats), nil
	}
	svc, err := p.localService()
	if err != nil {
		return "", err
	}
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
	return formatSyncStats(stats), nil
}

func formatSyncStats(stats SyncStats) string {
	target := strings.TrimSpace(stats.Target)
	if target == "" {
		target = "issues"
	}
	return fmt.Sprintf("Synced %s: %d imported, %d updated (%d issues)", target, stats.Imported, stats.Updated, stats.Total)
}

func formatSyncAllResult(result SyncAllResult) string {
	keys := make([]string, 0, len(result.Results)+len(result.Errors))
	for provider := range result.Results {
		keys = append(keys, provider)
	}
	for provider := range result.Errors {
		if _, ok := result.Results[provider]; !ok {
			keys = append(keys, provider)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "No providers synced.\n"
	}
	var b strings.Builder
	for _, provider := range keys {
		if stats, ok := result.Results[provider]; ok {
			fmt.Fprintf(&b, "%s: %s\n", provider, formatSyncStats(stats))
		}
		if message := result.Errors[provider]; message != "" {
			fmt.Fprintf(&b, "%s: %s\n", provider, message)
		}
	}
	return b.String()
}
func tuiServerURL(settings Settings) string {
	info, err := readServerInfo(settings.serverPath())
	if err != nil || strings.TrimSpace(info.URL) == "" {
		return ""
	}
	if info.PID > 0 && !processAlive(info.PID) {
		return ""
	}
	return info.URL
}

func tuiURLDetail(url string) string {
	if strings.TrimSpace(url) == "" {
		return "not running"
	}
	return url
}
