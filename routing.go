package main

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/peggco/pegg/pkg/plugin/hooks"
)

func (s *Service) changeRequestRoutes(ctx context.Context) map[string]string {
	routes := map[string]string{}
	if s.hookRunner != nil {
		outcome, err := s.hookRunner.FilterInto(ctx, HookChangeRequestRoutes, routes, &routes)
		switch {
		case errors.Is(err, hooks.ErrNoHandler):
		case err != nil:
			log.Printf("kanban: change request routes hook failed: %v", err)
		case len(outcome.Errors) > 0:
			log.Printf("kanban: change request routes hook errors: %s", strings.Join(outcome.Errors, "; "))
		}
	}
	if routes == nil {
		routes = map[string]string{}
	}
	for key, value := range s.settings.ChangeRequestProviders {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		routes[key] = strings.ToLower(strings.TrimSpace(value))
	}
	return routes
}

func (s *Service) resolveChangeRequestProvider(ctx context.Context, card *Card) string {
	if card == nil || card.Issue == nil {
		return strings.TrimSpace(s.settings.ChangeRequestProvider)
	}
	provider := strings.ToLower(strings.TrimSpace(card.Issue.Provider))
	if provider == "" {
		return strings.TrimSpace(s.settings.ChangeRequestProvider)
	}
	if mapped, ok := s.changeRequestRoutes(ctx)[provider]; ok {
		return strings.TrimSpace(mapped)
	}
	return provider
}
