# Context

Super Productivity's Eisenhower board, as Claude Code sees it through this MCP server.

## Language

**Task**:
A top-level Super Productivity task. Subtasks never appear on the board.
_Avoid_: item, todo

**Urgent**:
A task carries the `EM_URGENT` tag.

**Important**:
A task carries the `EM_IMPORTANT` tag.

**Quadrant**:
The Eisenhower board panel a task appears in, determined only by whether it is urgent and whether it is important. Panels: "Urgent and important", "Not urgent, important", "Urgent, not important", "Neither".
_Avoid_: Do / Schedule / Delegate / Delete, priority

**Move**:
Change a task's urgent and important flags, leaving its other tags alone.
_Avoid_: re-tag, reprioritise

**Complete**:
Mark a task done. A completed task stays in its quadrant until archived.

## Relationships

- A **Task** is in exactly one **Quadrant** at a time.
- A **Quadrant** exists only once both the `EM_URGENT` and `EM_IMPORTANT` tags exist in the sync account.
