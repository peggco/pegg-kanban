package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

//go:embed web/index.html web/app.js web/style.css
var webFS embed.FS

type sseEvent struct {
	name string
	data []byte
}

type Service struct {
	settings   Settings
	store      *Store
	runner     *Runner
	hookRunner hookRunner

	mu       sync.Mutex
	server   *http.Server
	listener net.Listener
	url      string
	port     int
	started  time.Time
	stopSync context.CancelFunc
	syncWG   sync.WaitGroup

	sseMu      sync.Mutex
	sseClients map[int]chan sseEvent
	nextClient int
}

func NewService(settings Settings) (*Service, error) {
	settings.normalize()
	s := &Service{
		settings:   settings,
		store:      NewStore(settings.boardPath()),
		hookRunner: newHooksClient(settings),
		sseClients: map[int]chan sseEvent{},
		started:    time.Now(),
	}
	s.runner = NewRunner(s.store, settings)
	s.runner.SetOnChange(func() {
		if board, err := s.store.Load(); err == nil {
			s.broadcastBoard(board)
		}
	})
	s.runner.SetOnSuccess(s.runSuccessHooks)
	s.runner.SetPrepareRun(s.prepareRun)
	return s, nil
}

func (s *Service) Store() *Store   { return s.store }
func (s *Service) Runner() *Runner { return s.runner }

func (s *Service) Settings() Settings {
	return s.settings
}

func (s *Service) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.server != nil {
		s.mu.Unlock()
		return nil
	}
	host := s.settings.Host
	token := s.settings.APIToken
	s.mu.Unlock()

	if !isLoopbackHost(host) && token == "" {
		return fmt.Errorf("refusing to bind non-loopback host %q without an API token", host)
	}
	if err := s.runner.MarkInterrupted(); err != nil {
		return err
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(s.settings.Port)))
	if err != nil {
		return fmt.Errorf("listen on %s:%d: %w", host, s.settings.Port, err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	urlHost := host
	if urlHost == "0.0.0.0" || urlHost == "::" || urlHost == "" {
		urlHost = "127.0.0.1"
	}
	schemeURL := "http://" + net.JoinHostPort(urlHost, strconv.Itoa(actualPort))

	srv := &http.Server{Handler: s.handler()}
	s.mu.Lock()
	s.server = srv
	s.listener = listener
	s.url = schemeURL
	s.port = actualPort
	s.started = time.Now()
	s.mu.Unlock()
	s.runner.SetServerURL(schemeURL)

	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("kanban: server stopped: %v", err)
		}
	}()
	s.startSyncLoop(ctx)
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	stopSync := s.stopSync
	srv := s.server
	s.server = nil
	s.url = ""
	s.mu.Unlock()
	if stopSync != nil {
		stopSync()
	}
	s.syncWG.Wait()
	s.runner.Shutdown()
	if s.hookRunner != nil {
		_ = s.hookRunner.Close()
	}
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Service) startSyncLoop(ctx context.Context) {
	providers, err := s.Providers(ctx)
	if err != nil {
		log.Printf("kanban: sync provider discovery failed: %v", err)
		return
	}
	parent, cancel := context.WithCancel(ctx)
	started := false
	for _, info := range providers {
		minutes := info.SyncMinutes
		if minutes <= 0 {
			minutes = s.settings.SyncMinutes
		}
		if !info.Enabled || !info.Available || minutes <= 0 {
			continue
		}
		started = true
		s.syncWG.Add(1)
		go func(provider string, minutes int) {
			defer s.syncWG.Done()
			s.runSyncLoop(parent, provider, minutes)
		}(info.ID, minutes)
	}
	if !started {
		cancel()
		return
	}
	s.mu.Lock()
	s.stopSync = cancel
	s.mu.Unlock()
}

func (s *Service) runSyncLoop(ctx context.Context, provider string, minutes int) {
	s.syncProviderOnce(ctx, provider)
	ticker := time.NewTicker(time.Duration(minutes) * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncProviderOnce(ctx, provider)
		}
	}
}

func (s *Service) syncProviderOnce(ctx context.Context, provider string) {
	syncCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := s.SyncProvider(syncCtx, SyncRequest{Provider: provider}); err != nil {
		log.Printf("kanban: scheduled %s sync failed: %v", provider, err)
	}
}

func (s *Service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serveStatic)

	api := http.NewServeMux()
	api.HandleFunc("GET /health", s.handleHealth)
	api.HandleFunc("GET /board", s.handleBoard)
	api.HandleFunc("POST /cards", s.handleCreateCard)
	api.HandleFunc("GET /cards/{id}", s.handleGetCard)
	api.HandleFunc("PATCH /cards/{id}", s.handlePatchCard)
	api.HandleFunc("DELETE /cards/{id}", s.handleDeleteCard)
	api.HandleFunc("POST /cards/{id}/move", s.handleMoveCard)
	api.HandleFunc("POST /cards/{id}/run", s.handleRunCard)
	api.HandleFunc("POST /cards/{id}/cancel", s.handleCancelCard)
	api.HandleFunc("POST /cards/{id}/retry", s.handleRetryCard)
	api.HandleFunc("GET /cards/{id}/log", s.handleCardLog)
	api.HandleFunc("POST /sync", s.handleSync)
	api.HandleFunc("GET /events", s.handleEvents)
	api.HandleFunc("GET /config", s.handleConfig)

	mux.Handle("/api/", s.middleware(http.StripPrefix("/api", api)))
	return mux
}

func (s *Service) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("kanban: panic serving %s: %v", r.URL.Path, rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal server error"})
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		if !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Printf("kanban: %s %s %d", r.Method, r.URL.Path, rec.status)
	})
}

func (s *Service) authorized(r *http.Request) bool {
	if s.settings.APIToken == "" {
		return true
	}
	token := r.Header.Get("X-PegG-Token")
	if token == "" {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.settings.APIToken)) == 1
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Service) serveStatic(w http.ResponseWriter, r *http.Request) {
	var name, contentType string
	switch r.URL.Path {
	case "/", "/index.html":
		name, contentType = "web/index.html", "text/html; charset=utf-8"
	case "/app.js":
		name, contentType = "web/app.js", "application/javascript; charset=utf-8"
	case "/style.css":
		name, contentType = "web/style.css", "text/css; charset=utf-8"
	case "/favicon.ico":
		w.WriteHeader(http.StatusNoContent)
		return
	default:
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Service) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": version,
		"pid":     os.Getpid(),
		"url":     s.URL(),
		"board":   s.store.Path(),
	})
}

func (s *Service) handleBoard(w http.ResponseWriter, r *http.Request) {
	board, err := s.store.Load()
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, board)
}

type createCardRequest struct {
	Title    string    `json:"title"`
	Body     string    `json:"body"`
	Column   string    `json:"column"`
	Priority string    `json:"priority"`
	Labels   []string  `json:"labels"`
	Assignee string    `json:"assignee"`
	Issue    *IssueRef `json:"issue"`
	Position int       `json:"position"`
	Source   string    `json:"source"`
}

func (s *Service) handleCreateCard(w http.ResponseWriter, r *http.Request) {
	var req createCardRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Title) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "title is required"})
		return
	}
	board, err := s.store.Load()
	if err != nil {
		writeError(w, err)
		return
	}
	column := req.Column
	if column == "" {
		column = board.Columns[0].ID
	}
	if _, ok := findColumn(board, column); !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown column: " + column})
		return
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}
	if req.Issue != nil {
		provider := strings.ToLower(strings.TrimSpace(req.Issue.Provider))
		req.Issue.Provider = provider
		if provider != "" {
			source = provider
		}
	}
	card := Card{
		ID:       newCardID(),
		Title:    strings.TrimSpace(req.Title),
		Body:     req.Body,
		Column:   column,
		Priority: req.Priority,
		Labels:   req.Labels,
		Assignee: req.Assignee,
		Source:   source,
		Issue:    req.Issue,
		Run:      RunState{Status: RunIdle},
	}
	if _, err := s.store.Add(card); err != nil {
		writeError(w, err)
		return
	}
	created, err := s.store.Card(card.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(created)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Service) handleGetCard(w http.ResponseWriter, r *http.Request) {
	card, err := s.store.Card(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (s *Service) handlePatchCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var patch struct {
		Title    *string   `json:"title"`
		Body     *string   `json:"body"`
		Priority *string   `json:"priority"`
		Labels   *[]string `json:"labels"`
		Assignee *string   `json:"assignee"`
		Column   *string   `json:"column"`
		Position *int      `json:"position"`
	}
	if !decodeJSON(w, r, &patch) {
		return
	}
	_, err := s.store.Update(func(b *Board) error {
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
		writeError(w, err)
		return
	}
	if patch.Column != nil {
		position := -1
		if patch.Position != nil {
			position = *patch.Position
		}
		if _, _, err := s.store.Move(id, *patch.Column, position); err != nil {
			writeError(w, err)
			return
		}
		if board, err := s.store.Load(); err == nil && *patch.Column == s.triggerColumn(board) {
			if moved, err := s.store.Card(id); err == nil && isRunnable(moved.Run.Status) {
				if err := s.runner.Trigger(id); err != nil {
					log.Printf("kanban: trigger %s after patch: %v", id, err)
				}
			}
		}
	}
	card, err := s.store.Card(id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(card)
	writeJSON(w, http.StatusOK, card)
}

func (s *Service) handleDeleteCard(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.Delete(r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleMoveCard(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Column   string `json:"column"`
		Position int    `json:"position"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Column == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "column is required"})
		return
	}
	id := r.PathValue("id")
	_, card, err := s.store.Move(id, req.Column, req.Position)
	if err != nil {
		writeError(w, err)
		return
	}
	board, err := s.store.Load()
	if err == nil && req.Column == s.triggerColumn(board) {
		status := card.Run.Status
		if status == "" || status == RunIdle || status == RunFailed || status == RunCancelled || status == RunSucceeded {
			if err := s.runner.Trigger(id); err != nil {
				log.Printf("kanban: trigger %s after move: %v", id, err)
			}
		}
	}
	card, err = s.store.Card(id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(card)
	writeJSON(w, http.StatusOK, card)
}

func (s *Service) handleRunCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runner.Trigger(id); err != nil {
		writeError(w, err)
		return
	}
	card, err := s.store.Card(id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(card)
	writeJSON(w, http.StatusAccepted, card)
}

func (s *Service) handleCancelCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runner.Cancel(id); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	card, err := s.store.Card(id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(card)
	writeJSON(w, http.StatusOK, card)
}

func (s *Service) handleRetryCard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.runner.Retry(id); err != nil {
		writeError(w, err)
		return
	}
	card, err := s.store.Card(id)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcastCard(card)
	writeJSON(w, http.StatusAccepted, card)
}

func (s *Service) handleCardLog(w http.ResponseWriter, r *http.Request) {
	card, err := s.store.Card(r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	tail := 200
	if value := r.URL.Query().Get("tail"); value != "" {
		if n, err := strconv.Atoi(value); err == nil && n > 0 {
			tail = n
		}
	}
	logPath := s.store.LogPath(card.ID)
	text := lastLines(logPath, tail)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(text))
}

func (s *Service) handleSync(w http.ResponseWriter, r *http.Request) {
	var req SyncRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = "all"
	}
	if provider == "all" {
		result, err := s.SyncAllProviders(r.Context(), req)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": redactSecrets(err.Error())})
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	providers, err := s.Providers(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": redactSecrets(err.Error())})
		return
	}
	info, ok := selectProvider(providers, provider)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "unknown sync provider: " + provider})
		return
	}
	if !info.Enabled {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sync provider " + info.ID + " is disabled"})
		return
	}
	if !info.Available {
		detail := strings.TrimSpace(info.Detail)
		if detail == "" {
			detail = "provider is not available"
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sync provider " + info.ID + " is unavailable: " + detail})
		return
	}
	req.Provider = info.ID
	stats, err := s.SyncProvider(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": redactSecrets(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Service) handleConfig(w http.ResponseWriter, r *http.Request) {
	config := s.settings.sanitized()
	providers, err := s.Providers(r.Context())
	if err != nil {
		config["providers"] = []ProviderInfo{}
		config["providers_error"] = redactSecrets(err.Error())
	} else {
		config["providers"] = providers
	}
	writeJSON(w, http.StatusOK, config)
}

func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan sseEvent, 16)
	s.sseMu.Lock()
	id := s.nextClient
	s.nextClient++
	s.sseClients[id] = ch
	s.sseMu.Unlock()
	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, id)
		s.sseMu.Unlock()
	}()

	board, err := s.store.Load()
	if err != nil {
		return
	}
	if data, err := json.Marshal(board); err == nil {
		fmt.Fprintf(w, "event: board\ndata: %s\n\n", data)
		flusher.Flush()
	}

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Service) broadcastBoard(board *Board) {
	data, err := json.Marshal(board)
	if err != nil {
		return
	}
	s.broadcast(sseEvent{name: "board", data: data})
}

func (s *Service) broadcastCard(card *Card) {
	data, err := json.Marshal(card)
	if err != nil {
		return
	}
	s.broadcast(sseEvent{name: "card", data: data})
}

func (s *Service) broadcast(ev sseEvent) {
	s.sseMu.Lock()
	defer s.sseMu.Unlock()
	for _, ch := range s.sseClients {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Service) triggerColumn(board *Board) string {
	if _, ok := findColumn(board, s.settings.TriggerColumn); ok {
		return s.settings.TriggerColumn
	}
	for _, col := range board.Columns {
		if col.Trigger {
			return col.ID
		}
	}
	return "in_progress"
}

func (s *Service) runSuccessHooks(card *Card, runContext *RunContext) {
	if card == nil {
		return
	}
	timeout := time.Duration(s.settings.HooksTimeout) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	safeContext := (*RunContext)(nil)
	if runContext != nil {
		copy := *runContext
		copy.Env = nil
		safeContext = &copy
	}
	payload := CardSucceeded{
		Card:                *card,
		Context:             safeContext,
		Summary:             feedbackSummary(s.store.LogPath(card.ID)),
		Feedback:            s.settings.Feedback,
		CreateChangeRequest: s.settings.CreateChangeRequest,
		TransitionOnSuccess: s.settings.TransitionOnSuccess,
		BaseBranch:          s.baseBranch(runContext),
	}
	outcome, err := s.hookRunner.Apply(ctx, HookCardSucceeded, hooks.HookAction, payload)
	if err != nil {
		log.Printf("kanban: card success hook failed: %v", err)
	} else if len(outcome.Errors) > 0 {
		log.Printf("kanban: card success hook errors: %s", strings.Join(outcome.Errors, "; "))
	}
}

func (s *Service) baseBranch(runContext *RunContext) string {
	if branch := strings.TrimSpace(s.settings.BaseBranch); branch != "" {
		return branch
	}
	if runContext != nil {
		if branch := strings.TrimSpace(runContext.Base); branch != "" {
			return branch
		}
	}
	return "main"
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrCardNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrColumnNotFound):
		status = http.StatusBadRequest
	case strings.Contains(err.Error(), "trigger column"), strings.Contains(err.Error(), "not queued or running"):
		status = http.StatusConflict
	case strings.Contains(err.Error(), "title"):
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{"error": redactSecrets(err.Error())})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.Body == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "request body is required"})
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]any{"error": "invalid JSON: " + err.Error()})
		return false
	}
	return true
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.Trim(host, "[]"))
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Service) currentPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.port > 0 {
		return s.port
	}
	return s.settings.Port
}

func (s *Service) serverJSON() map[string]any {
	return map[string]any{
		"pid":        os.Getpid(),
		"host":       s.settings.Host,
		"port":       s.currentPort(),
		"url":        s.URL(),
		"started_at": s.started.UTC().Format(time.RFC3339),
		"workdir":    s.settings.Workdir,
		"board":      s.store.Path(),
	}
}

func (s *Service) writeServerFile() error {
	data, err := json.MarshalIndent(s.serverJSON(), "", "  ")
	if err != nil {
		return err
	}
	path := s.settings.serverPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
