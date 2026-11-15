package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type serverInfo struct {
	PID       int    `json:"pid"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	URL       string `json:"url"`
	StartedAt string `json:"started_at"`
	Workdir   string `json:"workdir"`
	Board     string `json:"board,omitempty"`
	Embedded  bool   `json:"embedded,omitempty"`
}

type healthInfo struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	URL     string `json:"url"`
	Board   string `json:"board"`
}

func runCLI(args []string) int {
	if len(args) == 0 {
		printCLIHelp()
		return 0
	}
	if args[0] == "kanban" {
		args = args[1:]
	}
	if len(args) == 0 {
		printCLIHelp()
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		printCLIHelp()
		return 0
	}

	settings := defaultSettings()
	settings.applyConfigFile()
	settings.applyEnv()
	sub := args[0]
	flags, positional := parseFlags(args[1:])
	if flags["help"] == "true" || flags["h"] == "true" {
		printCLIHelp()
		return 0
	}
	if err := applyCLIFlags(&settings, flags); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	settings.normalize()

	if path, err := os.Executable(); err == nil {
		settings.PluginPath = path
	}

	if sub == "serve" {
		open := boolFlag(flags, "open", false) && flags["no-open"] != "true"
		if err := serveForeground(settings, open); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

	out, err := runKanbanCommand(settings, sub, positional, flags)
	if out != "" {
		fmt.Print(out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func applyCLIFlags(settings *Settings, flags map[string]string) error {
	if value := flagValue(flags, "workdir"); value != "" {
		value = expandHome(value)
		abs, err := filepath.Abs(value)
		if err != nil {
			return err
		}
		settings.Workdir = abs
	}
	if value := flagValue(flags, "host"); value != "" {
		settings.Host = value
	}
	if value := flagValue(flags, "port"); value != "" {
		port, err := strconv.Atoi(value)
		if err != nil || port <= 0 || port > 65535 {
			return fmt.Errorf("invalid --port: %s", value)
		}
		settings.Port = port
	}
	if value := flagValue(flags, "token"); value != "" {
		settings.APIToken = value
	}
	if value := flagValue(flags, "pegg-bin"); value != "" {
		settings.PeggBinary = expandHome(value)
	}
	if value := flagValue(flags, "max-parallel"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid --max-parallel: %s", value)
		}
		settings.MaxParallel = n
	}
	if value := flagValue(flags, "base"); value != "" {
		settings.BaseBranch = value
	}
	if value := flagValue(flags, "trigger-column"); value != "" {
		settings.TriggerColumn = value
	}
	if value := flagValue(flags, "review-column"); value != "" {
		settings.ReviewColumn = value
	}
	return nil
}

func parseFlags(args []string) (map[string]string, []string) {
	flags := map[string]string{}
	var positional []string
	boolFlags := map[string]bool{
		"foreground": true, "open": true, "no-open": true, "all": true,
		"json": true, "help": true, "h": true, "draft": true, "verbose": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" {
			flags["h"] = "true"
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimPrefix(arg, "--")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			flags[name[:eq]] = name[eq+1:]
			continue
		}
		if boolFlags[name] {
			value := "true"
			if i+1 < len(args) && (args[i+1] == "true" || args[i+1] == "false") {
				value = args[i+1]
				i++
			}
			flags[name] = value
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			flags[name] = args[i+1]
			i++
			continue
		}
		flags[name] = "true"
	}
	return flags, positional
}

func serveForeground(settings Settings, open bool) error {
	svc, err := NewService(settings)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := svc.Start(ctx); err != nil {
		return err
	}
	if err := svc.writeServerFile(); err != nil {
		fmt.Fprintf(os.Stderr, "kanban: write server metadata: %v\n", err)
	}
	fmt.Printf("Kanban server: %s\nBoard: %s\n", svc.URL(), svc.store.Path())
	if open {
		openBrowser(browserURL(svc.URL(), svc.settings.APIToken))
	}
	<-ctx.Done()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = svc.Stop(stopCtx)
	removeServerFileIfOurs(settings)
	return nil
}

func detectRunningServer(settings Settings) (string, bool) {
	if info, err := readServerInfo(settings.serverPath()); err == nil && info.URL != "" {
		if health, err := healthCheck(settings, info.URL); err == nil && health.OK {
			return health.URL, true
		}
		_ = os.Remove(settings.serverPath())
	}
	if health, err := healthCheck(settings, baseURL(settings)); err == nil && health.OK && health.Board == settings.boardPath() {
		return health.URL, true
	}
	return "", false
}

func commandStart(settings Settings, open bool) (string, error) {
	settings.normalize()
	if url, ok := detectRunningServer(settings); ok {
		if open {
			openBrowser(browserURL(url, settings.APIToken))
		}
		return url, nil
	}

	if err := os.MkdirAll(settings.stateDir(), 0o755); err != nil {
		return "", fmt.Errorf("create kanban state dir: %w", err)
	}
	bin := settings.PluginPath
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate plugin binary: %w", err)
		}
		bin = exe
	}
	serverLog, err := os.OpenFile(settings.serverLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", fmt.Errorf("open server log: %w", err)
	}
	defer serverLog.Close()

	args := []string{
		"serve", "--foreground",
		"--host", settings.Host,
		"--port", strconv.Itoa(settings.Port),
		"--workdir", settings.Workdir,
		"--max-parallel", strconv.Itoa(settings.MaxParallel),
	}
	if settings.PeggBinary != "" {
		args = append(args, "--pegg-bin", settings.PeggBinary)
	}
	if settings.BaseBranch != "" {
		args = append(args, "--base", settings.BaseBranch)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = serverLog
	cmd.Stderr = serverLog
	configureDetached(cmd)
	env := os.Environ()
	childSettings := settings
	childSettings.APIToken = ""
	if encoded, err := json.Marshal(childSettings); err == nil {
		env = setEnv(env, "PEGG_KANBAN_OPTIONS", string(encoded))
	}
	if settings.APIToken != "" {
		env = setEnv(env, "PEGG_KANBAN_TOKEN", settings.APIToken)
	}
	env = setEnv(env, "PEGG_PLUGIN", "")
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start kanban server: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	health, err := waitForHealth(settings, baseURL(settings), 5*time.Second)
	if err != nil {
		signalProcess(pid, false)
		tail := lastLines(settings.serverLogPath(), 40)
		if tail != "" {
			return "", fmt.Errorf("kanban server did not start: %w\n%s", err, tail)
		}
		return "", fmt.Errorf("kanban server did not start: %w", err)
	}
	url := health.URL
	if url == "" {
		url = baseURL(settings)
	}
	if health.PID > 0 {
		pid = health.PID
	}
	info := serverInfo{
		PID:       pid,
		Host:      settings.Host,
		Port:      settings.Port,
		URL:       url,
		StartedAt: nowRFC3339(),
		Workdir:   settings.Workdir,
		Board:     settings.boardPath(),
	}
	if err := writeServerInfo(settings.serverPath(), info); err != nil {
		return "", err
	}
	if open {
		openBrowser(browserURL(url, settings.APIToken))
	}
	return url, nil
}

func commandStop(settings Settings) (string, error) {
	info, err := readServerInfo(settings.serverPath())
	if err != nil {
		if os.IsNotExist(err) {
			return "Kanban server is not running\n", nil
		}
		return "", err
	}
	if info.Embedded {
		return "Kanban server is managed by its Pegg window; close that window to stop it\n", nil
	}
	health, healthErr := healthCheck(settings, info.URL)
	pid := info.PID
	if healthErr == nil && health.PID > 0 {
		pid = health.PID
	}
	if pid <= 0 || (healthErr != nil && !processAlive(pid)) {
		_ = os.Remove(settings.serverPath())
		return "Kanban server is not running (removed stale metadata)\n", nil
	}
	signalProcess(pid, false)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		signalProcess(pid, true)
	}
	_ = os.Remove(settings.serverPath())
	return fmt.Sprintf("Stopped Kanban server (pid %d)\n", pid), nil
}

func commandStatus(settings Settings) (string, error) {
	settings.normalize()
	store := NewStore(settings.boardPath())
	board, err := store.Load()
	if err != nil {
		return "", err
	}
	info, err := readServerInfo(settings.serverPath())
	if err != nil {
		return fmt.Sprintf("Kanban server: not running\nBoard: %s\nCards: %d\n", settings.boardPath(), len(board.Cards)), nil
	}
	health, healthErr := healthCheck(settings, info.URL)
	if healthErr != nil {
		return fmt.Sprintf("Kanban server: not running (stale metadata)\nBoard: %s\n", settings.boardPath()), nil
	}
	counts := map[string]int{}
	for _, card := range board.Cards {
		counts[card.Column]++
	}
	var parts []string
	for _, col := range board.Columns {
		parts = append(parts, fmt.Sprintf("%s=%d", col.ID, counts[col.ID]))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Kanban server: running\n")
	fmt.Fprintf(&b, "URL: %s\n", health.URL)
	fmt.Fprintf(&b, "PID: %d\n", health.PID)
	fmt.Fprintf(&b, "Board: %s\n", settings.boardPath())
	fmt.Fprintf(&b, "Cards: %d\n", len(board.Cards))
	fmt.Fprintf(&b, "Columns: %s\n", strings.Join(parts, " "))
	if len(board.Sync) > 0 {
		keys := make([]string, 0, len(board.Sync))
		for provider := range board.Sync {
			keys = append(keys, provider)
		}
		sort.Strings(keys)
		for _, provider := range keys {
			state := board.Sync[provider]
			if state.LastSync == "" {
				continue
			}
			fmt.Fprintf(&b, "%s: %s (%s, imported %d, updated %d)\n", provider, state.Target, state.LastStatus, state.Imported, state.Updated)
		}
	}
	return b.String(), nil
}

func baseURL(settings Settings) string {
	host := settings.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(settings.Port))
}

func healthCheck(settings Settings, base string) (*healthInfo, error) {
	if base == "" {
		base = baseURL(settings)
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+"/api/health", nil)
	if err != nil {
		return nil, err
	}
	if settings.APIToken != "" {
		req.Header.Set("X-PegG-Token", settings.APIToken)
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned %s", resp.Status)
	}
	var health healthInfo
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		return nil, err
	}
	if !health.OK {
		return nil, fmt.Errorf("health returned not ok")
	}
	return &health, nil
}

func waitForHealth(settings Settings, base string, timeout time.Duration) (*healthInfo, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		health, err := healthCheck(settings, base)
		if err == nil {
			return health, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return nil, lastErr
}

func readServerInfo(path string) (*serverInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info serverInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func writeServerInfo(path string, info serverInfo) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func removeServerFileIfOurs(settings Settings) {
	info, err := readServerInfo(settings.serverPath())
	if err != nil {
		return
	}
	if info.PID == os.Getpid() {
		_ = os.Remove(settings.serverPath())
	}
}

func openBrowser(url string) {
	if strings.TrimSpace(url) == "" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func runningServerURL(settings Settings) string {
	if info, err := readServerInfo(settings.serverPath()); err == nil && info.URL != "" {
		if health, err := healthCheck(settings, info.URL); err == nil && health.OK {
			return info.URL
		}
	}
	base := baseURL(settings)
	if health, err := healthCheck(settings, base); err == nil && health.OK {
		return health.URL
	}
	return ""
}

func serverRequest(settings Settings, method, base, path string, payload any, out any) error {
	return serverRequestContext(context.Background(), settings, method, base, path, payload, out)
}

func serverRequestContext(ctx context.Context, settings Settings, method, base, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if settings.APIToken != "" {
		req.Header.Set("X-PegG-Token", settings.APIToken)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &apiErr)
		message := strings.TrimSpace(apiErr.Error)
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("kanban server %d: %s", resp.StatusCode, redactSecrets(message))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func triggerColumnFor(settings Settings, board *Board) string {
	if _, ok := findColumn(board, settings.TriggerColumn); ok {
		return settings.TriggerColumn
	}
	for _, column := range board.Columns {
		if column.Trigger {
			return column.ID
		}
	}
	return "in_progress"
}

func browserURL(url, token string) string {
	if token == "" {
		return url
	}
	separator := "?"
	if strings.Contains(url, "?") {
		separator = "&"
	}
	return url + separator + "token=" + token
}

func printCLIHelp() {
	fmt.Print(`Usage: pegg-kanban [kanban] <command> [flags]

Commands:
  kanban                 Start the detached server and open the board
  kanban start           Start the detached Kanban web server
  kanban stop            Stop the detached server
  kanban status          Show server and board status
  kanban add <title>     Add a card (--column --body --priority --labels --assignee)
  kanban list            List cards (--column, --all)
  kanban move <id> <col> Move a card
  kanban sync            Sync integration issues
                         (--provider --target --query --labels --state --limit)
  kanban config          Show sanitized configuration
  kanban logs <id>       Print a card's agent log (--tail)
  kanban open            Open the board in a browser
  serve --foreground     Run the server in the foreground for debugging

Flags:
  --host, --port, --workdir, --token, --pegg-bin, --max-parallel,
  --base,
  --trigger-column, --review-column, --open, --no-open

Environment:
  PEGG_KANBAN_HOST, PEGG_KANBAN_PORT, PEGG_KANBAN_TOKEN,
  PEGG_KANBAN_MAX_PARALLEL, PEGG_KANBAN_PEGG_BIN, PEGG_HOOKS_BIN,
  PEGG_KANBAN_BOARD, PEGG_KANBAN_SERVER
`)
}
