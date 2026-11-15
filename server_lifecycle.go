package main

import (
	"context"
	"fmt"
	"os"
	"time"
)

func (p *Plugin) startServer(open bool) (string, error) {
	settings := p.currentSettings()
	settings.normalize()
	if url, ok := detectRunningServer(settings); ok {
		if open {
			openBrowser(browserURL(url, settings.APIToken))
		}
		return url, nil
	}

	p.mu.Lock()
	svc := p.service
	p.mu.Unlock()
	if svc != nil && svc.URL() != "" {
		url := svc.URL()
		if open {
			openBrowser(browserURL(url, settings.APIToken))
		}
		return url, nil
	}
	if svc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = svc.Stop(ctx)
		cancel()
	}

	svc, err := NewService(settings)
	if err != nil {
		return "", err
	}
	if err := svc.Start(context.Background()); err != nil {
		return "", fmt.Errorf("start kanban server: %w", err)
	}
	p.mu.Lock()
	p.service = svc
	p.ownsServer = true
	p.mu.Unlock()

	info := serverInfo{
		PID:       os.Getpid(),
		Host:      settings.Host,
		Port:      svc.port,
		URL:       svc.URL(),
		StartedAt: nowRFC3339(),
		Workdir:   settings.Workdir,
		Board:     settings.boardPath(),
		Embedded:  true,
	}
	if err := writeServerInfo(settings.serverPath(), info); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = svc.Stop(ctx)
		cancel()
		p.mu.Lock()
		p.service = nil
		p.ownsServer = false
		p.mu.Unlock()
		return "", err
	}
	if open {
		openBrowser(browserURL(svc.URL(), settings.APIToken))
	}
	return svc.URL(), nil
}

func (p *Plugin) stopServer() (string, error) {
	settings := p.currentSettings()
	p.mu.Lock()
	svc := p.service
	owned := p.ownsServer
	p.ownsServer = false
	p.mu.Unlock()
	if !owned || svc == nil {
		return commandStop(settings)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := svc.Stop(ctx)
	cancel()
	removeServerFileIfOurs(settings)
	p.mu.Lock()
	p.service = nil
	p.mu.Unlock()
	if err != nil {
		return "", err
	}
	return "Stopped Kanban server\n", nil
}
