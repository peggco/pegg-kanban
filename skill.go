package main

import peggplugin "github.com/peggco/pegg/pkg/plugin"

func kanbanSkill() peggplugin.SkillSpec {
	return peggplugin.SkillSpec{
		Name:        "kanban",
		Description: "Operate the local Kanban board through the kanban_* tools. Cards live in five columns and one card equals one agent run.",
		WhenToUse:   "Use when the user asks about the board, wants to add or move cards, start or cancel an agent run, inspect run logs, or sync issues from integration hooks onto the board.",
		ToolBundles: []string{toolBundle},
		Context:     "inline",
		AllowedTools: []string{
			"kanban_status", "kanban_list", "kanban_add", "kanban_update", "kanban_move",
			"kanban_run", "kanban_cancel", "kanban_log", "kanban_sync",
			"kanban_config",
		},
		Instructions: `# Kanban

The Kanban board is a local, file-backed task board at <workdir>/.pegg/kanban/board.json.
It has five default columns: backlog, ready, in_progress (trigger), review, and done
(terminal). Cards store title, description, priority, labels, an optional issue reference,
an optional run environment reference, and run state.

## Trigger semantics

- Moving a card into the trigger column (default in_progress) starts an agent run.
- The agent runs "pegg run --auto <prompt>" in the repository root, or in a run
  environment supplied by the installed integration plugin through kanban.run.prepare.
- A successful run moves the card to review when auto_move_on_success is enabled.
  A failed run stays in the trigger column with run.status=failed and the log tail in
  run.error.
- Only max_parallel runs execute at once; extra cards wait with run.status=queued.
- Cancel kills the whole process group. Retry resets the run and starts again.

## Optional run environments

- The kanban.run.prepare filter can provide a different working directory, branch,
  environment variables, or metadata. A worktree is one possible environment; a
  container or sandbox is another.
- Without a handler the agent runs in the repository root.
- Never create or remove worktrees by hand.
- Never edit files in another card's run environment.

## Working the board

1. Call kanban_status or kanban_list to see the current state.
2. Add work with kanban_add. Include a clear title and enough description for an
   isolated agent to finish the task without follow-up questions.
3. Move cards with kanban_move. Use kanban_update for editable fields.
4. Start explicitly with kanban_run; inspect progress with kanban_log (tail), then
   cancel with kanban_cancel if the run is wrong.
5. When a run finishes, review the log and move the card to done or back to ready.

## Issue sync

kanban_sync imports issues from the providers registered through the
'kanban.providers' and 'kanban.issues.fetch' hooks. Use provider=all to sync every
available provider, or pass a provider id. Imported cards carry issue.provider,
issue.repo/target, issue.number or issue.key, and issue.url. Integration plugins own
credentials and provider settings. When a linked card succeeds, the
'kanban.card.succeeded' action hook lets the matching integration comment on the issue,
transition it, or open a change request.

## Server and tools modes

- "pegg kanban" starts a detached local server and opens the web UI.
- Inside a card's agent run, PEGG_KANBAN_SERVER is set and the kanban_* tools proxy to
  that live server. Outside a run, the tools operate on the local board file directly.
- Never print API tokens. kanban_config is already sanitized.
`,
	}
}
