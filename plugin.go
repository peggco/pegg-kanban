package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

type Plugin struct {
	peggplugin.Base

	mu            sync.Mutex
	settings      Settings
	service       *Service
	providerCache []ProviderInfo
	ownsServer    bool
}

func (p *Plugin) Name() string { return "kanban" }

func (p *Plugin) Version() string { return version }

func (p *Plugin) Description() string {
	return "Drag-and-drop Kanban board with a local web UI that runs the Pegg agent with optional integration-provided run environments and syncs issues through integration hooks"
}

func (p *Plugin) Boot(deps peggplugin.Deps) error {
	settings := defaultSettings()
	if deps.Workdir != "" {
		settings.Workdir = deps.Workdir
	}
	if len(deps.Options) > 0 && string(deps.Options) != "null" {
		if err := json.Unmarshal(deps.Options, &settings); err != nil {
			return fmt.Errorf("parse kanban options: %w", err)
		}
	} else {
		settings.applyHost(deps.ConfigJSON)
	}
	if deps.PluginPath != "" {
		settings.PluginPath = deps.PluginPath
	} else if path, err := os.Executable(); err == nil {
		settings.PluginPath = path
	}
	settings.applyEnv()
	settings.normalize()

	p.mu.Lock()
	p.settings = settings
	p.service = nil
	p.mu.Unlock()
	return nil
}

func (p *Plugin) Capabilities() []string {
	return []string{
		string(peggplugin.CapabilityTools),
		string(peggplugin.CapabilitySkills),
		string(peggplugin.CapabilityPermissions),
		string(peggplugin.CapabilityCommands),
		string(peggplugin.CapabilityTUI),
	}
}

func (p *Plugin) Close() error {
	p.mu.Lock()
	svc := p.service
	owned := p.ownsServer
	p.service = nil
	p.ownsServer = false
	p.mu.Unlock()
	if svc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := svc.Stop(ctx)
	if owned {
		removeServerFileIfOurs(p.currentSettings())
	}
	return err
}

func (p *Plugin) Tools() ([]peggplugin.ToolSpec, error) {
	if !p.currentSettings().Enabled {
		return nil, nil
	}
	return toolSpecs(), nil
}

func (p *Plugin) Skills() ([]peggplugin.SkillSpec, error) {
	return []peggplugin.SkillSpec{kanbanSkill()}, nil
}

func (p *Plugin) PermissionRules() (map[string]string, error) {
	return permissionRules(), nil
}

func (p *Plugin) Commands() ([]peggplugin.CommandSpec, error) {
	return commandSpecs(), nil
}

func (p *Plugin) ExecuteTool(req peggplugin.ToolRequest) (*peggplugin.ToolResult, error) {
	settings := p.currentSettings()
	if !settings.Enabled {
		return nil, fmt.Errorf("kanban is disabled")
	}
	timeout := 120 * time.Second
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.executeTool(ctx, req.Name, req.Args)
}

func (p *Plugin) RunCommand(req peggplugin.CommandRequest) (*peggplugin.CommandResult, error) {
	out, err := p.runCommand(req)
	if err != nil {
		return nil, err
	}
	return &peggplugin.CommandResult{Output: out}, nil
}

func (p *Plugin) TUIPages() ([]peggplugin.TUIPage, error) {
	page, err := p.kanbanTUIPage()
	if err != nil {
		return nil, err
	}
	return []peggplugin.TUIPage{page}, nil
}

func (p *Plugin) ExecuteTUIAction(req peggplugin.TUIActionRequest) (*peggplugin.TUIActionResult, error) {
	return p.executeTUIAction(req)
}

func (p *Plugin) currentSettings() Settings {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.settings
}

func (p *Plugin) localService() (*Service, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.service == nil {
		svc, err := NewService(p.settings)
		if err != nil {
			return nil, err
		}
		p.service = svc
	}
	return p.service, nil
}
