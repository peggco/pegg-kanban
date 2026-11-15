package main

import (
	"encoding/json"

	peggplugin "github.com/peggco/pegg/pkg/plugin"
)

const toolBundle = "kanban"

func toolSpecs() []peggplugin.ToolSpec {
	return []peggplugin.ToolSpec{
		spec("kanban_status", "Show the Kanban board server status, board path, columns, card counts, and active runs.", nil),
		spec("kanban_list", "List cards on the board, optionally filtered by column or run status.", props{
			"column": str("Column id to filter by, e.g. backlog, ready, in_progress, review, done"),
			"status": str("Run status to filter by: idle, queued, running, succeeded, failed, cancelled"),
		}),
		spec("kanban_add", "Create a card. If issue fields are set, the card is linked to that issue.", props{
			"title":          str("Card title"),
			"body":           str("Card description / task details"),
			"column":         str("Destination column id (default: backlog)"),
			"priority":       enum("Card priority", "low", "medium", "high", "urgent"),
			"labels":         stringArray("Labels"),
			"assignee":       str("Assignee name"),
			"issue_provider": str("Issue provider id from kanban_status providers"),
			"issue_repo":     str("Issue repository or project target"),
			"issue_number":   integer("Issue number"),
			"issue_key":      str("Issue key for key-based trackers"),
			"issue_url":      str("Issue URL"),
		}, "title"),
		spec("kanban_update", "Update editable card fields. Moving a card to a new column can also start the agent when it lands in the trigger column.", props{
			"id":       str("Card id"),
			"title":    str("New title"),
			"body":     str("New description"),
			"priority": enum("Card priority", "low", "medium", "high", "urgent"),
			"labels":   stringArray("New labels"),
			"assignee": str("New assignee"),
			"column":   str("Move to this column"),
			"position": integer("Position within the destination column"),
		}, "id"),
		spec("kanban_move", "Move a card to a column and position. Dropping into the trigger column starts the agent run.", props{
			"id":       str("Card id"),
			"column":   str("Destination column id"),
			"position": integer("Zero-based position in the destination column; omit for the end"),
		}, "id", "column"),
		spec("kanban_run", "Start the agent for a card in the trigger column. Uses an integration-provided run environment when available.", props{
			"id": str("Card id"),
		}, "id"),
		spec("kanban_cancel", "Cancel a queued or running card. Kills the agent process group.", props{
			"id": str("Card id"),
		}, "id"),
		spec("kanban_log", "Return the last lines of a card's agent log.", props{
			"id":   str("Card id"),
			"tail": integer("Number of trailing lines (default 200)"),
		}, "id"),
		spec("kanban_sync", "Import and update cards from integration issues through the configured hooks.", props{
			"provider": str("Provider id from kanban_status providers, or all"),
			"target":   str("Repository, project, or tracker target; defaults to the provider target"),
			"labels":   stringArray("Only sync issues with these labels"),
			"state":    str("Issue state; defaults to the provider or Kanban setting"),
			"limit":    integer("Maximum issues to fetch"),
			"query":    str("Provider-specific query override"),
		}),
		spec("kanban_config", "Show the sanitized Kanban configuration. Never includes the API token.", nil),
	}
}

type props map[string]any

func spec(name, description string, properties props, required ...string) peggplugin.ToolSpec {
	if properties == nil {
		properties = props{}
	}
	payload := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		payload["required"] = required
	}
	encoded, _ := json.Marshal(payload)
	return peggplugin.ToolSpec{
		Name:        name,
		Description: description,
		Parameters:  string(encoded),
		Bundle:      toolBundle,
		Group:       "Kanban",
		ReadOnly:    readOnlyTool(name),
	}
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func boolean(description string) map[string]any {
	return map[string]any{"type": "boolean", "description": description}
}

func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

func enum(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func stringArray(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}

func readOnlyTool(name string) bool {
	switch name {
	case "kanban_status", "kanban_list", "kanban_log", "kanban_config":
		return true
	default:
		return false
	}
}

func permissionRules() map[string]string {
	rules := map[string]string{}
	for _, tool := range toolSpecs() {
		if tool.ReadOnly {
			rules[tool.Name] = "allow"
		} else {
			rules[tool.Name] = "semi-judge"
		}
	}
	return rules
}
