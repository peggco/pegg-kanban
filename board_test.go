package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, ".pegg", "kanban", "board.json"))
}

func addTestCard(t *testing.T, store *Store, title, column string) *Card {
	t.Helper()
	if _, err := store.Add(Card{Title: title, Column: column}); err != nil {
		t.Fatalf("add card: %v", err)
	}
	board, err := store.Load()
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	for i := range board.Cards {
		if board.Cards[i].Title == title {
			card := board.Cards[i]
			return &card
		}
	}
	t.Fatalf("card %q not found", title)
	return nil
}

func TestDefaultBoard(t *testing.T) {
	store := newTestStore(t)
	board, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if board.Version != 1 {
		t.Fatalf("version = %d, want 1", board.Version)
	}
	if board.ID == "" || board.Name == "" {
		t.Fatalf("board identity missing: %+v", board)
	}
	wantColumns := []string{"backlog", "ready", "in_progress", "review", "done"}
	if len(board.Columns) != len(wantColumns) {
		t.Fatalf("columns = %d, want %d", len(board.Columns), len(wantColumns))
	}
	for i, id := range wantColumns {
		if board.Columns[i].ID != id {
			t.Fatalf("column %d = %s, want %s", i, board.Columns[i].ID, id)
		}
	}
	if len(board.Cards) != 0 {
		t.Fatalf("new board has %d cards", len(board.Cards))
	}

	card := addTestCard(t, store, "First", "")
	if card.Column != "backlog" {
		t.Fatalf("default column = %s, want backlog", card.Column)
	}
	if card.Source != "manual" || card.Run.Status != RunIdle {
		t.Fatalf("unexpected defaults: %+v", card)
	}
	if card.Position != 0 {
		t.Fatalf("position = %d, want 0", card.Position)
	}
}

func TestMoveRenumbersPositions(t *testing.T) {
	store := newTestStore(t)
	c1 := addTestCard(t, store, "one", "backlog")
	c2 := addTestCard(t, store, "two", "backlog")
	c3 := addTestCard(t, store, "three", "backlog")

	board, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{c1.ID, c2.ID, c3.ID} {
		if card := boardCard(t, board, id); card.Position != i {
			t.Fatalf("initial position of %s = %d, want %d", id, card.Position, i)
		}
	}

	if _, _, err := store.Move(c1.ID, "ready", 0); err != nil {
		t.Fatalf("move to ready: %v", err)
	}
	board, _ = store.Load()
	if got := positions(board, "backlog"); strings.Join(got, ",") != c2.ID+","+c3.ID {
		t.Fatalf("backlog order = %v", got)
	}
	for i, id := range positions(board, "backlog") {
		if p := cardPosition(board, id); p != i {
			t.Fatalf("backlog position of %s = %d, want %d", id, p, i)
		}
	}

	if _, _, err := store.Move(c2.ID, "backlog", 0); err != nil {
		t.Fatalf("move c2: %v", err)
	}
	board, _ = store.Load()
	if got := positions(board, "backlog"); strings.Join(got, ",") != c2.ID+","+c3.ID {
		t.Fatalf("after move backlog order = %v", got)
	}

	if _, _, err := store.Move(c2.ID, "backlog", 1); err != nil {
		t.Fatalf("move c2 again: %v", err)
	}
	board, _ = store.Load()
	if got := positions(board, "backlog"); strings.Join(got, ",") != c3.ID+","+c2.ID {
		t.Fatalf("after reorder = %v", got)
	}
	if p := cardPosition(board, c3.ID); p != 0 {
		t.Fatalf("c3 position = %d, want 0", p)
	}
	if p := cardPosition(board, c2.ID); p != 1 {
		t.Fatalf("c2 position = %d, want 1", p)
	}

	if _, _, err := store.Move(c2.ID, "missing", 0); !errors.Is(err, ErrColumnNotFound) {
		t.Fatalf("move to missing column error = %v", err)
	}
	if _, _, err := store.Move("card-nope", "ready", 0); !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("move missing card error = %v", err)
	}
}

func TestDeleteAndPersistence(t *testing.T) {
	store := newTestStore(t)
	c1 := addTestCard(t, store, "keep", "backlog")
	c2 := addTestCard(t, store, "remove", "backlog")
	if _, _, err := store.SetRun(c1.ID, func(run *RunState) {
		run.Status = RunSucceeded
		run.ExitCode = 0
	}); err != nil {
		t.Fatalf("set run: %v", err)
	}
	if _, err := store.Delete(c2.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	reloaded := NewStore(store.Path())
	board, err := reloaded.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(board.Cards) != 1 || board.Cards[0].ID != c1.ID {
		t.Fatalf("unexpected cards after reload: %+v", board.Cards)
	}
	if board.Cards[0].Run.Status != RunSucceeded {
		t.Fatalf("run state did not persist: %+v", board.Cards[0].Run)
	}
	if _, err := reloaded.Card(c2.ID); !errors.Is(err, ErrCardNotFound) {
		t.Fatalf("deleted card lookup error = %v", err)
	}
}

func TestConcurrentUpdatesUseFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".pegg", "kanban", "board.json")
	stores := []*Store{NewStore(path), NewStore(path)}
	const perStore = 10
	var wg sync.WaitGroup
	errCh := make(chan error, len(stores)*perStore)
	for _, store := range stores {
		for i := 0; i < perStore; i++ {
			wg.Add(1)
			go func(s *Store, n int) {
				defer wg.Done()
				_, err := s.Add(Card{Title: "card", Column: "backlog"})
				errCh <- err
			}(store, i)
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent add: %v", err)
		}
	}
	board, err := stores[0].Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Cards) != len(stores)*perStore {
		t.Fatalf("cards = %d, want %d", len(board.Cards), len(stores)*perStore)
	}
	seen := map[int]bool{}
	for _, card := range board.Cards {
		if seen[card.Position] {
			t.Fatalf("duplicate position %d", card.Position)
		}
		seen[card.Position] = true
	}
}

func TestAtomicWriteAndEventLog(t *testing.T) {
	store := newTestStore(t)
	card := addTestCard(t, store, "events", "backlog")
	if _, err := os.Stat(store.Path() + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary board file left behind: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read board: %v", err)
	}
	var decoded Board
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("board is not valid JSON: %v", err)
	}

	if _, _, err := store.Move(card.ID, "ready", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetRun(card.ID, func(run *RunState) { run.Status = RunRunning; run.PID = 42 }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetRun(card.ID, func(run *RunState) { run.Status = RunSucceeded }); err != nil {
		t.Fatal(err)
	}

	eventsPath := filepath.Join(store.StateDir(), "events.jsonl")
	data, err = os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Type   string `json:"type"`
			CardID string `json:"card_id"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("event line is not JSON: %q: %v", line, err)
		}
		if event.Type == "" {
			t.Fatalf("event missing type: %q", line)
		}
		types = append(types, event.Type)
	}
	want := []string{"card.created", "card.moved", "run.started", "run.finished"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", types, want)
	}
}

func TestSubscribeReceivesUpdates(t *testing.T) {
	store := newTestStore(t)
	var mu sync.Mutex
	var snapshots int
	cancel := store.Subscribe(func(*Board) {
		mu.Lock()
		snapshots++
		mu.Unlock()
	})
	defer cancel()
	addTestCard(t, store, "notify", "backlog")
	mu.Lock()
	defer mu.Unlock()
	if snapshots != 1 {
		t.Fatalf("subscriber calls = %d, want 1", snapshots)
	}
}

func boardCard(t *testing.T, board *Board, id string) *Card {
	t.Helper()
	for i := range board.Cards {
		if board.Cards[i].ID == id {
			return &board.Cards[i]
		}
	}
	t.Fatalf("card %s not found", id)
	return nil
}

func positions(board *Board, column string) []string {
	type item struct {
		id  string
		pos int
	}
	var items []item
	for _, card := range board.Cards {
		if card.Column == column {
			items = append(items, item{card.ID, card.Position})
		}
	}
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].pos < items[i].pos {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].id
	}
	return out
}

func cardPosition(board *Board, id string) int {
	for _, card := range board.Cards {
		if card.ID == id {
			return card.Position
		}
	}
	return -1
}
