# Forge Project

> This project is managed by Forge v2.1. Check `.forge/state.md` on session start.

## Quick Reference

```
/forge --status       # Show project progress
/forge --phase N      # Execute phase N
/forge --autonomous   # Auto-execute remaining phases
/forge --milestone    # Verify milestone integration
/forge --discuss N    # Capture phase decisions
```

## Session Start Protocol

1. Read `.forge/state.md` to understand current position
2. Check blockers and next action
3. Resume or start next phase as indicated

## Project State Files

- `.forge/project.json` — Project identity and config
- `.forge/roadmap.md` — Phase sequence with milestones
- `.forge/state.md` — Session continuity (current position, decisions, blockers)
- `.forge/phases/{NN}-{name}/` — Per-phase execution artifacts

## Rules

- Do NOT modify `.forge/roadmap.md` manually — use forge commands
- Do NOT delete `.forge/state.md` — it's the session bridge
- Phase execution goes through the forge pipeline — do not bypass
- Success criteria in roadmap must be user-observable behaviors
