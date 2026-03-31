# 파이프라인 운영 흐름 — 단계를 절대 건너뛰지 않는다

## Standard Pipeline (large)

```
1. TeamCreate: team_name "vela-pipeline"

[Research] — Subagent (Sonnet)
2. Researcher subagent 1명 소환 (model: "sonnet"):
   - 프로젝트 분석 수행
   - 요구사항 파악 → 코드베이스 탐색 → 의존성/제약 분석 → 결론
3. PM이 리포트를 검토하여 research.md 작성
4. `node .vela/cli/vela-engine.js review` → SDK Reviewer 실행 → review-research.md 생성
5. PM이 review 읽고 approve/reject 판단

[Plan] — Subagent (Sonnet)
6. Planner subagent (model: "sonnet") → plan.md
7. `node .vela/cli/vela-engine.js review` → SDK Reviewer 실행 → review-plan.md 생성
8. PM approve/reject

[Execute — 단일 모듈] — Subagent (Sonnet)
9. `node .vela/cli/vela-engine.js execute` → SDK Executor (Sonnet) 코드 구현
10. `node .vela/cli/vela-engine.js review` → SDK Reviewer 실행 → review-execute.md 생성
11. PM approve/reject

[Execute — CrossLayer/다중 모듈] — Teammate (Sonnet)
9. Teammate 3~5명 (model: "sonnet", team_name, isolation: "worktree")
10. `node .vela/cli/vela-engine.js review` → SDK Reviewer 실행 → review-execute.md 생성
11. PM approve/reject

12. TeamDelete
```

## Quick Pipeline (medium)
Plan: Planner subagent (Sonnet) + SDK Reviewer (`vela-engine.js review`)
Execute: SDK Executor (`vela-engine.js execute`) + SDK Reviewer (`vela-engine.js review`)
팀 소환 없음.

## Trivial Pipeline (small)
PM 직접 수행. 에이전트 소환 없음. 소스 코드 직접 접근 허용.

## Ralph Pipeline
execute → verify 자동 반복 (최대 10회).

## PM 승인 기준
- **APPROVE**: Reviewer 점수 20+/25, CRITICAL 0개
- **REJECT**: CRITICAL/HIGH 미해결

## UI 템플릿
모든 AskUserQuestion은 `.vela/references/interactive-ui.md`에서 읽어라.
.
