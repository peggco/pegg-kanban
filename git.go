package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func gitRepoRoot(dir string) (string, error) {
	out, err := gitRun(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", fmt.Errorf("not a git repository: %s", dir)
	}
	return root, nil
}

func gitRun(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return string(out), nil
}

func gitOK(dir string, args ...string) bool {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run() == nil
}

func gitRemoteTarget(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	root, err := gitRepoRoot(dir)
	if err != nil {
		return ""
	}
	if out, err := gitRun(root, "remote", "get-url", "origin"); err == nil {
		if target := repoRefFromRemote(out); target != "" {
			return target
		}
	}
	names, err := gitRun(root, "remote")
	if err != nil {
		return ""
	}
	for _, name := range strings.Fields(names) {
		if name == "origin" {
			continue
		}
		if out, err := gitRun(root, "remote", "get-url", name); err == nil {
			if target := repoRefFromRemote(out); target != "" {
				return target
			}
		}
	}
	return ""
}

func repoRefFromRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var path string
	switch {
	case strings.HasPrefix(raw, "git@") || (strings.Contains(raw, ":") && !strings.Contains(raw, "://")):
		rest := raw
		if at := strings.Index(raw, "@"); at >= 0 {
			rest = raw[at+1:]
		}
		if colon := strings.Index(rest, ":"); colon >= 0 {
			path = rest[colon+1:]
		}
	case strings.Contains(raw, "://"):
		rest := raw[strings.Index(raw, "://")+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			path = rest[slash+1:]
		} else {
			return ""
		}
	default:
		path = raw
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	path = strings.TrimPrefix(path, "scm/")
	parts := strings.Split(path, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	if len(out) < 2 {
		return ""
	}
	return strings.Join(out, "/")
}
