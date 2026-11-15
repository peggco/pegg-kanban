package main

import (
	"os"
	"path/filepath"
	"testing"
)

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := gitRun(dir, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitRun(dir, "config", "user.email", "test@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitRun(dir, "config", "user.name", "Test"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := gitRun(dir, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := gitRun(dir, "commit", "-m", "init"); err != nil {
		t.Fatal(err)
	}
	return dir
}
