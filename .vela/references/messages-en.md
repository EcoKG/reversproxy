# Vela Engine — English Messages Reference

## Orchestrator
- `⛵ Vela — Explore mode. Reads allowed, writes blocked.`
- `🧭 To modify code: node .vela/cli/vela-engine.js init "<task>" --scale <small|medium|large>`

## Gate Keeper
- `⛵ [Vela] ✦ BLOCKED: Write operation in read-only mode.`
- `⛵ [Vela] ✦ BLOCKED: Bash is restricted in Vela sandbox.`
- `⛵ [Vela] ✦ BLOCKED: Cannot directly modify pipeline-state.json.`

## Gate Guard
- `🌟 [Vela] ✦ BLOCKED: No active pipeline (Explore mode).`
- `🧭 Solution: Start a pipeline with /vela start`
- `🌟 [Vela] ✦ BLOCKED: Claude task tools disabled during Vela pipeline.`

## AskUserQuestion Labels
- Pipeline start / Environment setup only
- Solo (direct analysis) / Subagent / 3 Teammates parallel (competing hypothesis)
- Small (trivial) / Medium (quick) / Large (standard) / Ralph
- Approve / Request changes / Cancel pipeline
- Use this message / Edit / Review diff first
- Create PR / Skip PR
- Auto-fix / Guide manually / Override / Cancel
