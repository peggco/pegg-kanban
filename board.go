package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Column struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Trigger  bool   `json:"trigger,omitempty"`
	Terminal bool   `json:"terminal,omitempty"`
	WIP      int    `json:"wip,omitempty"`
}

type IssueRef struct {
	Provider string `json:"provider,omitempty"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Key      string `json:"key,omitempty"`
	URL      string `json:"url"`
}

type WorktreeRef struct {
	Slug   string `json:"slug"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

type RunState struct {
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Log        string `json:"log,omitempty"`
	Error      string `json:"error,omitempty"`
	PID        int    `json:"pid,omitempty"`
}

type Card struct {
	ID        string       `json:"id"`
	Title     string       `json:"title"`
	Body      string       `json:"body,omitempty"`
	Column    string       `json:"column"`
	Position  int          `json:"position"`
	Priority  string       `json:"priority,omitempty"`
	Labels    []string     `json:"labels,omitempty"`
	Assignee  string       `json:"assignee,omitempty"`
	Source    string       `json:"source"`
	Issue     *IssueRef    `json:"issue,omitempty"`
	Context   *RunContext  `json:"context,omitempty"`
	Worktree  *WorktreeRef `json:"worktree,omitempty"`
	Run       RunState     `json:"run"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
}

type SyncState struct {
	LastSync   string `json:"last_sync,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	LastError  string `json:"last_error,omitempty"`
	Imported   int    `json:"imported,omitempty"`
	Updated    int    `json:"updated,omitempty"`
	Target     string `json:"target,omitempty"`
}

type Board struct {
	Version   int                  `json:"version"`
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Repo      string               `json:"repo,omitempty"`
	Columns   []Column             `json:"columns"`
	Cards     []Card               `json:"cards"`
	Sync      map[string]SyncState `json:"sync,omitempty"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt string               `json:"updated_at"`
}

var (
	ErrCardNotFound   = errors.New("kanban: card not found")
	ErrColumnNotFound = errors.New("kanban: column not found")
)

const (
	RunIdle      = "idle"
	RunQueued    = "queued"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func newBoardID() string {
	return fmt.Sprintf("board-%d", time.Now().UnixNano())
}

func newCardID() string {
	buf := make([]byte, 2)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("card-%d-0000", time.Now().UnixNano())
	}
	return fmt.Sprintf("card-%d-%s", time.Now().UnixNano(), hex.EncodeToString(buf))
}

func defaultColumns() []Column {
	return []Column{
		{ID: "backlog", Name: "Backlog"},
		{ID: "ready", Name: "Ready"},
		{ID: "in_progress", Name: "In Progress", Trigger: true},
		{ID: "review", Name: "Review"},
		{ID: "done", Name: "Done", Terminal: true},
	}
}

func defaultBoard() *Board {
	now := nowRFC3339()
	return &Board{
		Version:   1,
		ID:        newBoardID(),
		Name:      "Kanban",
		Columns:   defaultColumns(),
		Cards:     []Card{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func issueDisplayKey(issue *IssueRef) string {
	if issue == nil {
		return ""
	}
	if strings.TrimSpace(issue.Key) != "" {
		return issue.Key
	}
	if issue.Number > 0 {
		return fmt.Sprintf("%d", issue.Number)
	}
	return ""
}

func cardDisplayKey(card Card) string {
	return issueDisplayKey(card.Issue)
}

func findIssueCard(b *Board, provider, target string, number int, key string) *Card {
	key = strings.TrimSpace(key)
	provider = strings.TrimSpace(provider)
	target = strings.TrimSpace(target)
	for i := range b.Cards {
		card := &b.Cards[i]
		issue := card.Issue
		if issue == nil {
			continue
		}
		if provider != "" && issue.Provider != "" && !strings.EqualFold(issue.Provider, provider) {
			continue
		}
		if target != "" && issue.Repo != "" && !strings.EqualFold(issue.Repo, target) {
			continue
		}
		if key != "" {
			if strings.EqualFold(strings.TrimSpace(issue.Key), key) {
				return card
			}
			continue
		}
		if number > 0 && issue.Number == number {
			return card
		}
	}
	return nil
}

func normalizeBoard(b *Board) {
	if b.Version == 0 {
		b.Version = 1
	}
	if b.ID == "" {
		b.ID = newBoardID()
	}
	if strings.TrimSpace(b.Name) == "" {
		b.Name = "Kanban"
	}
	if len(b.Columns) == 0 {
		b.Columns = defaultColumns()
	}
	if b.Cards == nil {
		b.Cards = []Card{}
	}
	if b.Sync == nil {
		b.Sync = map[string]SyncState{}
	}
	now := nowRFC3339()
	if b.CreatedAt == "" {
		b.CreatedAt = now
	}
	if b.UpdatedAt == "" {
		b.UpdatedAt = now
	}
	for i := range b.Cards {
		c := &b.Cards[i]
		if c.ID == "" {
			c.ID = newCardID()
		}
		if c.Column == "" {
			c.Column = b.Columns[0].ID
		}
		if c.Source == "" {
			c.Source = "manual"
		}
		if c.Run.Status == "" {
			c.Run.Status = RunIdle
		}
		if c.CreatedAt == "" {
			c.CreatedAt = now
		}
		if c.UpdatedAt == "" {
			c.UpdatedAt = c.CreatedAt
		}
	}
}

type Store struct {
	path string

	mu          sync.Mutex
	subMu       sync.Mutex
	subscribers map[int]func(*Board)
	nextSub     int
}

func NewStore(path string) *Store {
	s := &Store{path: path, subscribers: map[int]func(*Board){}}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.MkdirAll(filepath.Join(filepath.Dir(path), "logs"), 0o755)
	return s
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) StateDir() string {
	return filepath.Dir(s.path)
}

func (s *Store) LogPath(cardID string) string {
	return filepath.Join(s.StateDir(), "logs", sanitizeFilename(cardID)+".log")
}

func sanitizeFilename(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "card"
	}
	return out
}

func (s *Store) Subscribe(fn func(*Board)) func() {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	id := s.nextSub
	s.nextSub++
	s.subscribers[id] = fn
	return func() {
		s.subMu.Lock()
		delete(s.subscribers, id)
		s.subMu.Unlock()
	}
}

func (s *Store) emit(b *Board) {
	s.subMu.Lock()
	subs := make([]func(*Board), 0, len(s.subscribers))
	for _, fn := range s.subscribers {
		subs = append(subs, fn)
	}
	s.subMu.Unlock()
	for _, fn := range subs {
		fn(b)
	}
}

func (s *Store) Load() (*Board, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultBoard(), nil
		}
		return nil, fmt.Errorf("read board: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return defaultBoard(), nil
	}
	var b Board
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse board: %w", err)
	}
	migrateLegacySync(data, &b)
	migrateLegacyWorktree(&b)
	normalizeBoard(&b)
	return &b, nil
}

type legacySyncState struct {
	LastSync   string `json:"last_sync"`
	LastStatus string `json:"last_status"`
	LastError  string `json:"last_error"`
	Imported   int    `json:"imported"`
	Updated    int    `json:"updated"`
	Repo       string `json:"repo"`
	Project    string `json:"project"`
}

func migrateLegacyWorktree(b *Board) {
	for i := range b.Cards {
		card := &b.Cards[i]
		if card.Context == nil && card.Worktree != nil {
			card.Context = &RunContext{
				Kind:   "worktree",
				Path:   card.Worktree.Path,
				Branch: card.Worktree.Branch,
				Label:  card.Worktree.Branch,
			}
		}
		card.Worktree = nil
	}
}

func migrateLegacySync(data []byte, b *Board) {
	if b.Sync == nil {
		b.Sync = map[string]SyncState{}
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		return
	}
	for _, key := range []string{"github", "gitlab", "jira"} {
		raw, ok := legacy[key]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var old legacySyncState
		if err := json.Unmarshal(raw, &old); err != nil {
			continue
		}
		if existing, ok := b.Sync[key]; ok && (existing.LastSync != "" || existing.LastStatus != "") {
			continue
		}
		target := old.Repo
		if target == "" {
			target = old.Project
		}
		if old.LastSync == "" && old.LastStatus == "" && old.LastError == "" && old.Imported == 0 && old.Updated == 0 && target == "" {
			continue
		}
		b.Sync[key] = SyncState{
			LastSync:   old.LastSync,
			LastStatus: old.LastStatus,
			LastError:  old.LastError,
			Imported:   old.Imported,
			Updated:    old.Updated,
			Target:     target,
		}
	}
}

func (s *Store) lock() (func(), error) {
	lockPath := s.path + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("lock board: %w", err)
		}
		if fi, statErr := os.Stat(lockPath); statErr == nil && time.Since(fi.ModTime()) > 10*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("board is locked: %s", lockPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (s *Store) write(b *Board) error {
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("encode board: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create board dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write board: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace board: %w", err)
	}
	return nil
}

func (s *Store) Update(fn func(*Board) error) (*Board, error) {
	s.mu.Lock()
	unlock, err := s.lock()
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	defer func() {
		unlock()
		s.mu.Unlock()
	}()

	b, err := s.Load()
	if err != nil {
		return nil, err
	}
	if err := fn(b); err != nil {
		return nil, err
	}
	normalizeBoard(b)
	b.UpdatedAt = nowRFC3339()
	if err := s.write(b); err != nil {
		return nil, err
	}
	s.emit(b)
	return b, nil
}

func (s *Store) Card(id string) (*Card, error) {
	b, err := s.Load()
	if err != nil {
		return nil, err
	}
	for i := range b.Cards {
		if b.Cards[i].ID == id {
			c := b.Cards[i]
			return &c, nil
		}
	}
	return nil, ErrCardNotFound
}

func (s *Store) Add(card Card) (*Board, error) {
	b, err := s.Update(func(b *Board) error {
		now := nowRFC3339()
		if card.ID == "" {
			card.ID = newCardID()
		}
		if card.Column == "" {
			card.Column = b.Columns[0].ID
		}
		if _, ok := findColumn(b, card.Column); !ok {
			return fmt.Errorf("%w: %s", ErrColumnNotFound, card.Column)
		}
		if card.Source == "" {
			card.Source = "manual"
		}
		if card.Run.Status == "" {
			card.Run.Status = RunIdle
		}
		if card.CreatedAt == "" {
			card.CreatedAt = now
		}
		card.UpdatedAt = now
		card.Position = countColumn(b, card.Column)
		b.Cards = append(b.Cards, card)
		renumberColumn(b, card.Column)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.appendEvent("card.created", card.ID, map[string]any{"title": card.Title, "column": card.Column})
	return b, nil
}

func (s *Store) Move(id, column string, position int) (*Board, *Card, error) {
	var moved Card
	b, err := s.Update(func(b *Board) error {
		if _, ok := findColumn(b, column); !ok {
			return fmt.Errorf("%w: %s", ErrColumnNotFound, column)
		}
		idx := cardIndex(b, id)
		if idx < 0 {
			return ErrCardNotFound
		}
		oldColumn := b.Cards[idx].Column
		destIDs := columnIDs(b, column, id)
		if position < 0 || position > len(destIDs) {
			position = len(destIDs)
		}
		destIDs = insertString(destIDs, position, id)

		b.Cards[idx].Column = column
		b.Cards[idx].UpdatedAt = nowRFC3339()
		renumberColumn(b, column)
		renumberOrder(b, column, destIDs)
		if oldColumn != column {
			renumberColumn(b, oldColumn)
		}
		moved = b.Cards[idx]
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	s.appendEvent("card.moved", id, map[string]any{"column": column, "position": moved.Position})
	return b, &moved, nil
}

func (s *Store) Delete(id string) (*Board, error) {
	var deleted bool
	b, err := s.Update(func(b *Board) error {
		idx := cardIndex(b, id)
		if idx < 0 {
			return ErrCardNotFound
		}
		column := b.Cards[idx].Column
		b.Cards = append(b.Cards[:idx], b.Cards[idx+1:]...)
		renumberColumn(b, column)
		deleted = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if deleted {
		s.appendEvent("card.deleted", id, nil)
	}
	return b, nil
}

func (s *Store) SetRun(id string, fn func(*RunState)) (*Board, *Card, error) {
	var updated Card
	b, err := s.Update(func(b *Board) error {
		idx := cardIndex(b, id)
		if idx < 0 {
			return ErrCardNotFound
		}
		fn(&b.Cards[idx].Run)
		if b.Cards[idx].Run.Status == "" {
			b.Cards[idx].Run.Status = RunIdle
		}
		b.Cards[idx].UpdatedAt = nowRFC3339()
		updated = b.Cards[idx]
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	switch updated.Run.Status {
	case RunRunning:
		s.appendEvent("run.started", id, map[string]any{"pid": updated.Run.PID})
	case RunSucceeded, RunFailed, RunCancelled:
		s.appendEvent("run.finished", id, map[string]any{"status": updated.Run.Status, "exit_code": updated.Run.ExitCode})
	}
	return b, &updated, nil
}

func (s *Store) SetMeta(fn func(*Board) error) (*Board, error) {
	return s.Update(fn)
}

func (s *Store) appendEvent(kind, cardID string, data map[string]any) {
	entry := map[string]any{
		"time":    nowRFC3339(),
		"type":    kind,
		"card_id": cardID,
	}
	if data != nil {
		entry["data"] = data
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.StateDir(), "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func findColumn(b *Board, id string) (Column, bool) {
	for _, col := range b.Columns {
		if col.ID == id {
			return col, true
		}
	}
	return Column{}, false
}

func cardIndex(b *Board, id string) int {
	for i := range b.Cards {
		if b.Cards[i].ID == id {
			return i
		}
	}
	return -1
}

func countColumn(b *Board, column string) int {
	n := 0
	for i := range b.Cards {
		if b.Cards[i].Column == column {
			n++
		}
	}
	return n
}

func columnIDs(b *Board, column, exclude string) []string {
	type positioned struct {
		id   string
		pos  int
		time string
	}
	var items []positioned
	for i := range b.Cards {
		if b.Cards[i].Column == column && b.Cards[i].ID != exclude {
			items = append(items, positioned{b.Cards[i].ID, b.Cards[i].Position, b.Cards[i].CreatedAt})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].pos != items[j].pos {
			return items[i].pos < items[j].pos
		}
		return items[i].time < items[j].time
	})
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].id
	}
	return ids
}

func renumberColumn(b *Board, column string) {
	renumberOrder(b, column, columnIDs(b, column, ""))
}

func renumberOrder(b *Board, column string, order []string) {
	pos := make(map[string]int, len(order))
	for i, id := range order {
		pos[id] = i
	}
	for i := range b.Cards {
		if b.Cards[i].Column != column {
			continue
		}
		if p, ok := pos[b.Cards[i].ID]; ok {
			b.Cards[i].Position = p
		}
	}
}

func insertString(items []string, index int, value string) []string {
	if index < 0 {
		index = 0
	}
	if index > len(items) {
		index = len(items)
	}
	items = append(items, "")
	copy(items[index+1:], items[index:])
	items[index] = value
	return items
}
