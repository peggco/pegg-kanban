package main

import "testing"

func TestRepoRefFromRemote(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/widgets.git":                   "acme/widgets",
		"https://github.com/acme/widgets":                       "acme/widgets",
		"git@github.com:acme/widgets.git":                       "acme/widgets",
		"ssh://git@bitbucket.example.com:7999/PROJ/widgets.git": "PROJ/widgets",
		"https://bitbucket.example.com/scm/PROJ/widgets.git":    "PROJ/widgets",
		"https://gitlab.com/group/sub/project.git":              "group/sub/project",
		"":          "",
		"not-a-url": "",
		"a/b":       "a/b",
	}
	for remote, want := range cases {
		if got := repoRefFromRemote(remote); got != want {
			t.Errorf("repoRefFromRemote(%q) = %q, want %q", remote, got, want)
		}
	}
}
