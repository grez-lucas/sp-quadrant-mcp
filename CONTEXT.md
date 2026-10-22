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

**Notes**:
A task's markdown notes field in SP: the context that does not fit the title.
_Avoid_: description, body

**Checklist**:
The `- [ ] ` items in a task's **Notes**, one per line. Not subtasks.
_Avoid_: subtasks, steps

## Relationships

- A **Task** is in exactly one **Quadrant** at a time.
- A **Task** has at most one **Notes** field, and a **Checklist** lives inside it.
- A **Quadrant** exists only once both the `EM_URGENT` and `EM_IMPORTANT` tags exist in the sync account.
