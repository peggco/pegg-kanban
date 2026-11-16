package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const version = "1.0.0"

type Settings struct {
	Enabled                bool              `json:"enabled"`
	Host                   string            `json:"host"`
	Port                   int               `json:"port"`
	APIToken               string            `json:"api_token"`
	AutoOpen               bool              `json:"auto_open"`
	PeggBinary             string            `json:"pegg_binary"`
	AgentArgs              []string          `json:"agent_args"`
	Provider               string            `json:"provider"`
	Model                  string            `json:"model"`
	MaxParallel            int               `json:"max_parallel"`
	TriggerColumn          string            `json:"trigger_column"`
	ReviewColumn           string            `json:"review_column"`
	AutoMoveOnSuccess      bool              `json:"auto_move_on_success"`
	AutoAnswer             bool              `json:"auto_answer"`
	AutoAnswerValue        string            `json:"auto_answer_value"`
	BaseBranch             string            `json:"base_branch"`
	SyncColumn             string            `json:"sync_column"`
	SyncMinutes            int               `json:"sync_minutes"`
	SyncUpdateExisting     bool              `json:"sync_update_existing"`
	SyncLabels             []string          `json:"sync_labels"`
	SyncLimit              int               `json:"sync_limit"`
	SyncState              string            `json:"sync_state"`
	Feedback               bool              `json:"feedback"`
	CreateChangeRequest    bool              `json:"create_change_request"`
	ChangeRequestProvider  string            `json:"change_request_provider"`
	ChangeRequestProviders map[string]string `json:"change_request_providers"`
	TransitionOnSuccess    string            `json:"transition_on_success"`
	HooksTimeout           int               `json:"hooks_timeout_seconds"`
	Workdir                string            `json:"-"`
	PluginPath             string            `json:"-"`
}

func defaultSettings() Settings {
	wd, _ := os.Getwd()
	binary := resolvePeggBinary()
	return Settings{
		Enabled:             true,
		Host:                "127.0.0.1",
		Port:                7878,
		AutoOpen:            true,
		PeggBinary:          binary,
		MaxParallel:         2,
		TriggerColumn:       "in_progress",
		ReviewColumn:        "review",
		AutoMoveOnSuccess:   true,
		AutoAnswer:          true,
		AutoAnswerValue:     "1",
		SyncColumn:          "backlog",
		SyncMinutes:         0,
		SyncUpdateExisting:  true,
		SyncLimit:           50,
		Feedback:            true,
		CreateChangeRequest: true,
		HooksTimeout:        90,
		Workdir:             wd,
	}
}

func (s *Settings) applyHost(configJSON []byte) {
	if len(configJSON) == 0 {
		return
	}
	var host struct {
		Plugins struct {
			Options map[string]json.RawMessage `json:"options"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(configJSON, &host); err != nil {
		return
	}
	for _, key := range []string{"kanban", "pegg-kanban"} {
		raw := host.Plugins.Options[key]
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		_ = json.Unmarshal(raw, s)
	}
}

func (s *Settings) applyConfigFile() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	data, err := os.ReadFile(filepath.Join(home, ".pegg", "pegg.json"))
	if err != nil {
		return
	}
	s.applyHost(data)
}

func (s *Settings) applyEnv() {
	if raw := os.Getenv("PEGG_KANBAN_OPTIONS"); strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), s)
	}
	if v := os.Getenv("PEGG_KANBAN_HOST"); v != "" {
		s.Host = v
	}
	if v := os.Getenv("PEGG_KANBAN_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.Port = n
		}
	}
	if v := os.Getenv("PEGG_KANBAN_TOKEN"); v != "" {
		s.APIToken = v
	}
	if v := os.Getenv("PEGG_KANBAN_MAX_PARALLEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.MaxParallel = n
		}
	}
	if v := os.Getenv("PEGG_KANBAN_PEGG_BIN"); v != "" {
		s.PeggBinary = v
	}
	if v := os.Getenv("PEGG_BIN"); v != "" && s.PeggBinary == "" {
		s.PeggBinary = v
	}
	if v := os.Getenv("PEGG_KANBAN_BASE"); v != "" {
		s.BaseBranch = v
	}
	if v := os.Getenv("PEGG_KANBAN_AUTO_OPEN"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.AutoOpen = b
		}
	}
	if v := os.Getenv("PEGG_KANBAN_AUTO_ANSWER"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.AutoAnswer = b
		}
	}
	if v := os.Getenv("PEGG_KANBAN_AUTO_ANSWER_VALUE"); v != "" {
		s.AutoAnswerValue = v
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_COLUMN"); v != "" {
		s.SyncColumn = v
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			s.SyncMinutes = n
		}
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_UPDATE_EXISTING"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.SyncUpdateExisting = b
		}
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_LABELS"); v != "" {
		s.SyncLabels = splitList(v)
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.SyncLimit = n
		}
	}
	if v := os.Getenv("PEGG_KANBAN_SYNC_STATE"); v != "" {
		s.SyncState = v
	}
	if v := os.Getenv("PEGG_KANBAN_FEEDBACK"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.Feedback = b
		}
	}
	if v := os.Getenv("PEGG_KANBAN_CREATE_CHANGE_REQUEST"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			s.CreateChangeRequest = b
		}
	}
	if v := os.Getenv("PEGG_KANBAN_CHANGE_REQUEST_PROVIDER"); v != "" {
		s.ChangeRequestProvider = v
	}
	if v := os.Getenv("PEGG_KANBAN_CHANGE_REQUEST_PROVIDERS"); strings.TrimSpace(v) != "" {
		routes := map[string]string{}
		if err := json.Unmarshal([]byte(v), &routes); err == nil {
			s.ChangeRequestProviders = routes
		}
	}
	if v := os.Getenv("PEGG_KANBAN_TRANSITION_ON_SUCCESS"); v != "" {
		s.TransitionOnSuccess = v
	}
	if v := os.Getenv("PEGG_HOOKS_TIMEOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.HooksTimeout = n
		}
	}
	s.normalize()
}

func (s *Settings) normalize() {
	if strings.TrimSpace(s.Host) == "" {
		s.Host = "127.0.0.1"
	}
	if s.Port <= 0 {
		s.Port = 7878
	}
	if s.MaxParallel <= 0 {
		s.MaxParallel = 2
	}
	if strings.TrimSpace(s.TriggerColumn) == "" {
		s.TriggerColumn = "in_progress"
	}
	if strings.TrimSpace(s.ReviewColumn) == "" {
		s.ReviewColumn = "review"
	}
	if strings.TrimSpace(s.SyncColumn) == "" {
		s.SyncColumn = "backlog"
	}
	if s.SyncMinutes < 0 {
		s.SyncMinutes = 0
	}
	if s.SyncLimit <= 0 {
		s.SyncLimit = 50
	}
	s.Workdir = expandHome(s.Workdir)
	if s.Workdir == "" {
		s.Workdir, _ = os.Getwd()
	}
	if abs, err := filepath.Abs(s.Workdir); err == nil {
		s.Workdir = abs
	}
	s.PeggBinary = expandHome(strings.TrimSpace(s.PeggBinary))
	if s.PeggBinary == "" {
		s.PeggBinary = resolvePeggBinary()
	}
	if s.HooksTimeout <= 0 {
		s.HooksTimeout = 90
	}
	s.ChangeRequestProvider = strings.ToLower(strings.TrimSpace(s.ChangeRequestProvider))
	s.ChangeRequestProviders = normalizeRoutes(s.ChangeRequestProviders)
}

func normalizeRoutes(routes map[string]string) map[string]string {
	if len(routes) == 0 {
		return nil
	}
	out := make(map[string]string, len(routes))
	for key, value := range routes {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		out[key] = strings.ToLower(strings.TrimSpace(value))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (s Settings) stateDir() string {
	return filepath.Dir(s.boardPath())
}

func (s Settings) boardPath() string {
	if v := strings.TrimSpace(os.Getenv("PEGG_KANBAN_BOARD")); v != "" {
		p := expandHome(v)
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	wd := s.Workdir
	if wd == "" {
		wd, _ = os.Getwd()
	}
	return filepath.Join(wd, ".pegg", "kanban", "board.json")
}

func (s Settings) logsDir() string {
	return filepath.Join(s.stateDir(), "logs")
}

func (s Settings) eventsPath() string {
	return filepath.Join(s.stateDir(), "events.jsonl")
}

func (s Settings) serverPath() string {
	return filepath.Join(s.stateDir(), "server.json")
}

func (s Settings) serverLogPath() string {
	return filepath.Join(s.stateDir(), "server.log")
}

func (s Settings) sanitized() map[string]any {
	return map[string]any{
		"enabled":                  s.Enabled,
		"host":                     s.Host,
		"port":                     s.Port,
		"token_configured":         s.APIToken != "",
		"auto_open":                s.AutoOpen,
		"pegg_binary":              s.PeggBinary,
		"agent_args":               s.AgentArgs,
		"provider":                 s.Provider,
		"model":                    s.Model,
		"max_parallel":             s.MaxParallel,
		"trigger_column":           s.TriggerColumn,
		"review_column":            s.ReviewColumn,
		"auto_move_on_success":     s.AutoMoveOnSuccess,
		"base_branch":              s.BaseBranch,
		"sync_column":              s.SyncColumn,
		"sync_minutes":             s.SyncMinutes,
		"sync_update_existing":     s.SyncUpdateExisting,
		"sync_labels":              s.SyncLabels,
		"sync_limit":               s.SyncLimit,
		"sync_state":               s.SyncState,
		"sync_target":              gitRemoteTarget(s.Workdir),
		"feedback":                 s.Feedback,
		"create_change_request":    s.CreateChangeRequest,
		"change_request_provider":  s.ChangeRequestProvider,
		"change_request_providers": s.ChangeRequestProviders,
		"transition_on_success":    s.TransitionOnSuccess,
		"hooks_timeout_seconds":    s.HooksTimeout,
		"board":                    s.boardPath(),
		"workdir":                  s.Workdir,
	}
}

func resolvePeggBinary() string {
	if v := strings.TrimSpace(os.Getenv("PEGG_BIN")); v != "" {
		return expandHome(v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".pegg", "bin", "pegg")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("pegg"); err == nil {
		return p
	}
	return "pegg"
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
