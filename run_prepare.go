package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

func (s *Service) prepareRun(ctx context.Context, card *Card, repo string) (*RunContext, error) {
	if s.hookRunner == nil || card == nil {
		return nil, nil
	}
	req := RunPrepare{
		Card:                  *card,
		Repo:                  repo,
		BaseBranch:            s.settings.BaseBranch,
		ChangeRequestProvider: s.resolveChangeRequestProvider(ctx, card),
		Context: RunContext{
			Kind:  "repo",
			Path:  repo,
			Base:  s.settings.BaseBranch,
			Label: "repository",
		},
	}
	outcome, err := s.hookRunner.FilterInto(ctx, HookRunPrepare, req, &req)
	if err != nil {
		if errors.Is(err, hooks.ErrNoHandler) {
			return nil, nil
		}
		return nil, err
	}
	if len(outcome.Errors) > 0 {
		return nil, errors.New(strings.Join(outcome.Errors, "; "))
	}
	if strings.TrimSpace(req.Context.Path) == "" {
		req.Context.Path = repo
	}
	if strings.TrimSpace(req.Context.Kind) == "" {
		req.Context.Kind = "repo"
	}
	if _, err := os.Stat(req.Context.Path); err != nil {
		return nil, fmt.Errorf("run context path %s: %w", req.Context.Path, err)
	}
	return &req.Context, nil
}
