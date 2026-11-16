package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

var ErrProviderNotFound = errors.New("kanban: sync provider not found")

type SyncRequest struct {
	Provider string   `json:"provider,omitempty"`
	Target   string   `json:"target,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	State    string   `json:"state,omitempty"`
	Limit    int      `json:"limit,omitempty"`
	Query    string   `json:"query,omitempty"`
}

type SyncStats struct {
	Provider string `json:"provider,omitempty"`
	Target   string `json:"target,omitempty"`
	Imported int    `json:"imported"`
	Updated  int    `json:"updated"`
	Total    int    `json:"total"`
}

type SyncAllResult struct {
	Provider string               `json:"provider,omitempty"`
	Results  map[string]SyncStats `json:"results"`
	Errors   map[string]string    `json:"errors,omitempty"`
}

func (s *Service) Providers(ctx context.Context) ([]ProviderInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var providers []ProviderInfo
	outcome, err := s.hookRunner.FilterInto(ctx, HookProviders, []ProviderInfo{}, &providers)
	if err != nil {
		return nil, err
	}
	if len(outcome.Errors) > 0 {
		log.Printf("kanban: provider discovery errors: %s", strings.Join(outcome.Errors, "; "))
	}
	repoTarget := s.syncTarget()
	for i := range providers {
		if !providers[i].RepoTarget {
			continue
		}
		if strings.TrimSpace(providers[i].Target) == "" {
			providers[i].Target = repoTarget
		}
		if strings.TrimSpace(providers[i].Target) == "" {
			providers[i].Available = false
			if strings.TrimSpace(providers[i].Detail) == "" {
				providers[i].Detail = "No git repository found; open the board inside a git repository"
			}
		}
	}
	return providers, nil
}

func selectProvider(providers []ProviderInfo, id string) (ProviderInfo, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProviderInfo{}, false
	}
	for _, info := range providers {
		if strings.EqualFold(info.ID, id) {
			return info, true
		}
	}
	return ProviderInfo{}, false
}

func (s *Service) SyncProvider(ctx context.Context, req SyncRequest) (SyncStats, error) {
	providers, err := s.Providers(ctx)
	if err != nil {
		return SyncStats{}, err
	}
	info, ok := selectProvider(providers, req.Provider)
	if !ok {
		return SyncStats{}, fmt.Errorf("%w: %s", ErrProviderNotFound, strings.TrimSpace(req.Provider))
	}
	if !info.Enabled {
		return SyncStats{}, fmt.Errorf("sync provider %s is disabled", info.ID)
	}
	if !info.Available {
		detail := strings.TrimSpace(info.Detail)
		if detail == "" {
			detail = "provider is not available"
		}
		return SyncStats{}, fmt.Errorf("sync provider %s is unavailable: %s", info.ID, detail)
	}
	return s.syncProvider(ctx, info, req)
}

func (s *Service) SyncAllProviders(ctx context.Context, req SyncRequest) (SyncAllResult, error) {
	providers, err := s.Providers(ctx)
	if err != nil {
		return SyncAllResult{}, err
	}
	result := SyncAllResult{
		Provider: "all",
		Results:  map[string]SyncStats{},
		Errors:   map[string]string{},
	}
	for _, info := range providers {
		if !info.Enabled || !info.Available {
			continue
		}
		stats, syncErr := s.syncProvider(ctx, info, req)
		if syncErr != nil {
			result.Errors[info.ID] = redactSecrets(syncErr.Error())
			continue
		}
		result.Results[info.ID] = stats
	}
	return result, nil
}

func (s *Service) syncProvider(ctx context.Context, info ProviderInfo, req SyncRequest) (SyncStats, error) {
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = strings.TrimSpace(info.Target)
	}
	if target == "" {
		target = s.syncTarget()
	}
	if target == "" {
		err := fmt.Errorf("%s sync: no git repository found in %s", info.ID, s.settings.Workdir)
		s.recordSyncError(info.ID, "", err)
		return SyncStats{}, err
	}
	labels := req.Labels
	if len(labels) == 0 {
		labels = s.settings.SyncLabels
	}
	state := strings.TrimSpace(req.State)
	if state == "" {
		state = s.settings.SyncState
	}
	limit := req.Limit
	if limit <= 0 {
		limit = s.settings.SyncLimit
	}
	fetch := IssueFetch{
		Provider: info.ID,
		Target:   target,
		Labels:   labels,
		State:    state,
		Limit:    limit,
		Query:    strings.TrimSpace(req.Query),
		Issues:   []HookIssue{},
	}
	outcome, err := s.hookRunner.FilterInto(ctx, HookIssueFetch, fetch, &fetch)
	if err != nil {
		s.recordSyncError(info.ID, target, err)
		return SyncStats{}, err
	}
	if len(outcome.Errors) > 0 {
		hookErr := errors.New(strings.Join(outcome.Errors, "; "))
		s.recordSyncError(info.ID, target, hookErr)
		return SyncStats{}, hookErr
	}
	if strings.TrimSpace(fetch.Target) == "" {
		fetch.Target = target
	}
	stats := SyncStats{Provider: info.ID, Target: fetch.Target, Total: len(fetch.Issues)}

	board, err := s.store.Load()
	if err != nil {
		s.recordSyncError(info.ID, fetch.Target, err)
		return stats, err
	}
	trigger := s.triggerColumn(board)
	column := strings.TrimSpace(s.settings.SyncColumn)
	if _, ok := findColumn(board, column); !ok {
		column = board.Columns[0].ID
	}
	updateExisting := s.settings.SyncUpdateExisting

	_, updateErr := s.store.Update(func(b *Board) error {
		for _, issue := range fetch.Issues {
			existing := findIssueCard(b, info.ID, fetch.Target, issue.Number, issue.Key)
			if existing == nil {
				card := Card{
					ID:       newCardID(),
					Title:    issueTitle(issue),
					Body:     issue.Body,
					Column:   column,
					Priority: issuePriority(issue),
					Labels:   append([]string(nil), issue.Labels...),
					Assignee: strings.Join(issue.Assignees, ", "),
					Source:   info.ID,
					Issue: &IssueRef{
						Provider: info.ID,
						Repo:     fetch.Target,
						Number:   issue.Number,
						Key:      issue.Key,
						URL:      issue.URL,
					},
					Run:       RunState{Status: RunIdle},
					CreatedAt: nowRFC3339(),
					UpdatedAt: nowRFC3339(),
				}
				if _, ok := findColumn(b, card.Column); !ok {
					card.Column = b.Columns[0].ID
				}
				card.Position = countColumn(b, card.Column)
				b.Cards = append(b.Cards, card)
				stats.Imported++
				continue
			}
			if !updateExisting || existing.Column == trigger || columnTerminal(b, existing.Column) {
				continue
			}
			existing.Title = issueTitle(issue)
			existing.Body = issue.Body
			existing.Labels = append([]string(nil), issue.Labels...)
			existing.Priority = issuePriority(issue)
			existing.Assignee = strings.Join(issue.Assignees, ", ")
			if existing.Issue == nil {
				existing.Issue = &IssueRef{}
			}
			existing.Issue.Provider = info.ID
			existing.Issue.Repo = fetch.Target
			if issue.Number > 0 {
				existing.Issue.Number = issue.Number
			}
			if issue.Key != "" {
				existing.Issue.Key = issue.Key
			}
			if issue.URL != "" {
				existing.Issue.URL = issue.URL
			}
			existing.UpdatedAt = nowRFC3339()
			stats.Updated++
		}
		if b.Sync == nil {
			b.Sync = map[string]SyncState{}
		}
		b.Sync[info.ID] = SyncState{
			LastSync:   nowRFC3339(),
			LastStatus: "ok",
			Imported:   stats.Imported,
			Updated:    stats.Updated,
			Target:     fetch.Target,
		}
		return nil
	})
	if updateErr != nil {
		s.recordSyncError(info.ID, fetch.Target, updateErr)
		return stats, updateErr
	}
	s.store.appendEvent("sync.completed", "", map[string]any{
		"provider": info.ID,
		"target":   fetch.Target,
		"imported": stats.Imported,
		"updated":  stats.Updated,
	})
	return stats, nil
}

func issueTitle(issue HookIssue) string {
	if title := strings.TrimSpace(issue.Title); title != "" {
		return title
	}
	if key := strings.TrimSpace(issue.Key); key != "" {
		return key
	}
	if issue.Number > 0 {
		return fmt.Sprintf("#%d", issue.Number)
	}
	return "Imported issue"
}

func issuePriority(issue HookIssue) string {
	if priority := strings.TrimSpace(issue.Priority); priority != "" {
		return priority
	}
	return "medium"
}

func (s *Service) recordSyncError(provider, target string, syncErr error) {
	_, _ = s.store.Update(func(b *Board) error {
		if b.Sync == nil {
			b.Sync = map[string]SyncState{}
		}
		state := b.Sync[provider]
		state.LastSync = nowRFC3339()
		state.LastStatus = "error"
		state.LastError = redactSecrets(syncErr.Error())
		if target != "" {
			state.Target = target
		}
		b.Sync[provider] = state
		return nil
	})
}

func columnTerminal(b *Board, id string) bool {
	col, ok := findColumn(b, id)
	return ok && col.Terminal
}

const (
	feedbackLogTail    = 64 * 1024
	maxFeedbackSummary = 4000
	maxToolResultBytes = 500
)

func buildFeedbackBody(card *Card, logPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Pegg finished card **%s**.\n\n", card.Title)
	if card.Context != nil && card.Context.Branch != "" {
		fmt.Fprintf(&b, "- Branch: `%s`\n", card.Context.Branch)
	}
	if summary := finalAgentOutput(readLogTail(logPath, feedbackLogTail)); summary != "" {
		if len(summary) > maxFeedbackSummary {
			summary = summary[:maxFeedbackSummary] + "\n..."
		}
		b.WriteString("\n```\n")
		b.WriteString(summary)
		b.WriteString("\n```\n")
	}
	return b.String()
}

func feedbackSummary(logPath string) string {
	summary := finalAgentOutput(readLogTail(logPath, feedbackLogTail))
	if len(summary) > maxFeedbackSummary {
		summary = summary[:maxFeedbackSummary] + "\n..."
	}
	return summary
}

func finalAgentOutput(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")

	anchor := -1
	for i, line := range lines {
		if isAgentTranscriptLine(line) {
			anchor = i
		}
	}
	if anchor < 0 {
		return trimAgentOutput(lines)
	}
	start := anchor + 1
	if isToolResultLine(lines[anchor]) {
		start = skipToolResultPayload(lines, anchor)
	}
	return trimAgentOutput(lines[start:])
}

func isAgentTranscriptLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "tool "),
		strings.HasPrefix(line, "result "),
		strings.HasPrefix(line, "Thinking"),
		strings.HasPrefix(line, "Running "),
		strings.HasPrefix(line, "Subagent "):
		return true
	case strings.HasPrefix(line, "• "):
		return !strings.Contains(line, " finished")
	}
	return false
}

func isToolResultLine(line string) bool {
	return strings.HasPrefix(line, "result ")
}

func skipToolResultPayload(lines []string, anchor int) int {
	consumed := 0
	for i := anchor + 1; i < len(lines); i++ {
		line := lines[i]
		if isAgentTranscriptLine(line) {
			return i
		}
		if strings.TrimSpace(line) == "" {
			return i + 1
		}
		if strings.HasSuffix(strings.TrimRight(line, " \t"), "...") {
			return i + 1
		}
		consumed += len(line) + 1
		if consumed > maxToolResultBytes {
			return anchor + 1
		}
	}
	return len(lines)
}

func trimAgentOutput(lines []string) string {
	start := 0
	for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	end := len(lines)
	for end > start {
		line := lines[end-1]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "• ") {
			end--
			continue
		}
		break
	}
	return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
}

func readLogTail(path string, maxBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if maxBytes > 0 && len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return string(data)
}

var (
	tokenPattern         = regexp.MustCompile(`(?i)(token|authorization|bearer|password|secret|api[_-]?key|ghp_[a-z0-9]+|github_pat_[a-z0-9_]+|glpat-[a-z0-9_-]+)[^\s]*`)
	credentialURLPattern = regexp.MustCompile(`(?i)(https?://)[^\s/@]+@`)
)

func redactSecrets(value string) string {
	value = credentialURLPattern.ReplaceAllString(value, "${1}[redacted]@")
	return tokenPattern.ReplaceAllString(value, "[redacted]")
}

var _ = hooks.HookFilter
