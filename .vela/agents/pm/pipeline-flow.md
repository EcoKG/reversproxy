# 파이프라인 흐름 v4 — discuss → plan → execute → verify → ship

## 흐름 개요

```
/vela:start "작업설명"
  └─ PM: 규모 선택 → ROADMAP.md, pipeline-state.json 생성

/vela:discuss N
  └─ PM ↔ 사용자 대화 (회색영역 해소, 요구사항 확정)
  └─ 산출물: .vela/artifacts/{N}-context.md

/vela:plan N
  ├─ Subagent(researcher-A)  읽음: {N}-context.md  →  씀: {N}-research-A.md  ┐ 병렬
  ├─ Subagent(researcher-B)  읽음: {N}-context.md  →  씀: {N}-research-B.md  ┘
  ├─ (researcher 1개면 synthesizer 생략)
  ├─ Subagent(synthesizer)   읽음: {N}-research-*.md → 씀: {N}-research.md
  └─ Subagent(planner)       읽음: {N}-context.md + {N}-research.md → 씀: {N}-plan.xml

/vela:execute N
  ├─ {N}-plan.xml 파싱 → Wave 그룹 확인
  ├─ Wave 1 (병렬): Subagent(executor-A) + Subagent(executor-B)
  │    각자: 읽음 {N}-plan.xml 해당 task
  │    → 코드 구현
  │    → git add + git commit (태스크 완료 즉시)
  │    → 씀: {N}-1-summary.md, {N}-2-summary.md
  └─ Wave 2 (순차, Wave 1 완료 후): Subagent(executor-C)
       읽음: {N}-1-summary.md + {N}-plan.xml task
       → 코드 구현 → git commit → 씀: {N}-3-summary.md

/vela:verify N
  └─ Subagent(verifier) 읽음: {N}-*-summary.md + {N}-context.md → 씀: {N}-verification.md
  └─ 사용자 확인 → /vela:next

/vela:ship N
  └─ PM 읽음: {N}-context.md + {N}-verification.md + {N}-*-summary.md
  └─ PR 본문 자동 생성 + gh pr create 실행

/vela:next  →  자동 감지  →  /vela:discuss N+1 또는 다음 단계
```

---

## 아티팩트 구조

```
.vela/artifacts/
  {N}-context.md          ← discuss 산출물 (사용자 확정 요구사항)
  {N}-research-A.md       ← researcher-A 산출물
  {N}-research-B.md       ← researcher-B 산출물
  {N}-research.md         ← synthesizer 종합
  {N}-plan.xml            ← planner 산출물 (Wave/task 구조화)
  {N}-{M}-summary.md      ← executor 산출물 (git 커밋됨)
  {N}-verification.md     ← verifier 산출물
  HANDOFF.json            ← pause 시 생성, resume 시 소비
```

---

## plan.xml 구조

```xml
<plan phase="N">
  <context>{N}-context.md 요약</context>
  <tasks>
    <task id="1" wave="1" depends="">
      <files>src/auth/jwt.js</files>
      <action>JWT 검증 미들웨어 구현</action>
      <verify>npm test -- --grep "auth"</verify>
      <done>인증 테스트 통과 + {N}-1-summary.md 작성</done>
    </task>
    <task id="2" wave="1" depends="">
      <files>src/models/user.js</files>
      <action>User 모델 구현</action>
      <verify>npm test -- --grep "user"</verify>
      <done>유저 테스트 통과 + {N}-2-summary.md 작성</done>
    </task>
    <task id="3" wave="2" depends="1,2">
      <files>src/api/auth.js</files>
      <action>인증 API 엔드포인트 구현</action>
      <verify>npm test -- --grep "api"</verify>
      <done>API 테스트 통과 + {N}-3-summary.md 작성</done>
    </task>
  </tasks>
</plan>
```

---

## git 커밋 전략

```
[execute 단계 — 태스크당 즉시 커밋]
feat({N}-1): implement JWT middleware
feat({N}-2): add user model
feat({N}-3): create auth endpoint
docs({N}): add execution summary

[ship 단계]
chore({N}): ship — PR #42

[milestone-complete]
chore: release v1.0.0  ← git tag 자동 생성
```

상세 규칙: `.vela/agents/pm/git-strategy.md`

---

## 옵션 플래그

| 플래그 | 단계 | 동작 |
|--------|------|------|
| `--no-research` | plan | researcher 소환 생략, planner 직접 실행 |
| `--skip-discuss` | start/discuss | discuss 없이 바로 plan |
| `--assumptions` | discuss | PM이 가정으로 context.md 작성 |
| `--auto-verify` | verify | 사용자 확인 없이 자동 통과 |
| `--draft` | ship | draft PR 생성 |
| `--full` | plan/execute/quick | 강화 검증 모드 활성화 |

### `--full` 플래그 상세

```
/vela:plan N --full
  → planner 완료 후 Subagent(plan-checker) 소환
  → 읽음: {N}-plan.xml + {N}-context.md
  → 요구사항 대비 plan 검증, 미달 시 planner 재소환 (최대 2회)

/vela:execute N --full
  → 각 Wave 완료 후 Subagent(mini-verifier) 소환
  → 읽음: 해당 Wave {N}-{M}-summary.md들
  → 실패 감지 시 Debugger 소환 후 다음 Wave 진행

/vela:quick "작업" --full
  → plan-checker 활성화 + 실행 후 verifier 자동 실행
```

---

## 자동 디버깅 — 테스트 실패 시

테스트가 실패하면 같은 Executor를 재시도하지 **않는다**.
**반드시 Debugger 에이전트를 소환**하여 근본 원인을 분석한다.

```
테스트 실패
  ↓
Debugger subagent 소환 (model: "opus")
  ↓
근본 원인 분석 → 최소 수정 → 테스트 재실행
  ↓
성공 → 계속 / 실패(3회) → 사용자에게 보고
```

Debugger 소환 프롬프트:
```xml
<task>
  <role>debugger</role>
  <model>opus</model>
  <files>{실패한 파일}</files>
  <action>
    에러: {에러 메시지}
    스택: {스택 트레이스}
    .vela/agents/debugger/index.md를 읽고 진단 절차를 따르세요.
  </action>
  <verify>{테스트 명령어}</verify>
  <done>모든 테스트 통과 + debug-report.md 작성</done>
</task>
```

---

## HANDOFF.json (pause/resume)

### pause 시 작성
```json
{
  "phase": 2,
  "step": "execute",
  "wave": 1,
  "completed_tasks": ["2-1", "2-2"],
  "pending_tasks": ["2-3"],
  "artifacts": [".vela/artifacts/2-context.md", ".vela/artifacts/2-plan.xml"],
  "last_commit": "abc123f",
  "paused_at": "2026-03-26T15:00:00Z"
}
```

### resume 시 처리
1. HANDOFF.json 읽기
2. `step` 확인 → 해당 단계로 이동
3. `pending_tasks` 확인 → 미완료 태스크부터 재개
4. 재개 완료 후 HANDOFF.json 삭제

---

## Quick Pipeline

`/vela:quick "작업"` = discuss 없이 자동 실행:
1. `$ARGUMENTS`로 바로 context.md 작성 (--assumptions 모드)
2. plan 실행
3. execute 실행
4. verify 실행 (--auto-verify 적용)
5. 완료 후 ship 여부 사용자에게 질문
