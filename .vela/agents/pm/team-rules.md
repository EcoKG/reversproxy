# 에이전트 위임 규칙 — Subagent 전용

## Subagent 전용 구조

v4부터 모든 에이전트는 **Subagent**로만 소환한다.
에이전트 간 SendMessage 소통은 사용하지 않는다.
**파일 기반 소통**: 에이전트가 `.vela/artifacts/`에 파일을 쓰고, PM이 읽어 다음 에이전트에 전달.

| 역할 | 방식 | model |
|------|------|-------|
| researcher | **Subagent** | `"opus"` |
| synthesizer | **Subagent** | `"sonnet"` |
| planner | **Subagent** | `"opus"` |
| executor | **Subagent** | `"sonnet"` |
| verifier | **Subagent** | `"sonnet"` |
| debugger | **Subagent** | `"opus"` |
| 파일 탐색/검색 | **Subagent** | `"haiku"` |

---

## 컨텍스트 Tier 로딩 — 에이전트에 필요한 것만 전달

에이전트 소환 시 **과도한 컨텍스트를 주지 않는다**. 단계별로 필수 컨텍스트만 전달한다.

| Tier | 포함 | 언제 |
|------|------|------|
| 0 (항상) | 아티팩트 경로, 현재 phase | 모든 소환 |
| 1 (리서치) | {N}-context.md 경로, 관점(architecture/security/quality) | plan 단계 |
| 2 (계획) | {N}-research.md 경로, {N}-context.md 경로 | plan 단계 |
| 3 (실행) | {N}-plan.xml 경로, 해당 task id, wave 번호 | execute 단계 |
| 4 (검증) | {N}-*-summary.md 경로들, {N}-context.md 경로 | verify 단계 |

**절대 하지 않을 것**: 전체 소스 코드를 프롬프트에 포함. 이전 단계의 모든 산출물을 한꺼번에 전달.

---

## XML 구조화 태스크 — 에이전트 소환 시 반드시 사용

에이전트에게 태스크를 전달할 때 **반드시 XML 구조**를 사용한다.

```xml
<task>
  <role>executor</role>
  <model>sonnet</model>
  <files>src/api/auth.js, src/middleware/jwt.js</files>
  <action>
    {N}-plan.xml의 task id="M"을 구현한다.
    구현 완료 후 git commit하고 {N}-M-summary.md를 작성한다.
  </action>
  <verify>npm test -- --grep "auth"</verify>
  <done>테스트 통과 + git commit 완료 + {N}-M-summary.md 작성됨</done>
</task>
```

### XML 필드 설명
- `<role>`: 에이전트 역할 (executor, researcher, planner 등)
- `<model>`: 사용할 모델 (반드시 지정)
- `<files>`: 대상 파일
- `<action>`: 구체적 작업 지시 (읽을 파일 경로 포함)
- `<verify>`: 검증 방법 (실행 가능한 명령어)
- `<done>`: 완료 조건 (산출물 파일 명시)

---

## 소환 프롬프트 — 반드시 이 형식을 따른다

```
.vela/agents/{role}/index.md를 읽고 필요한 가이드만 선택적으로 읽어라.

<task>
  <role>{role}</role>
  <model>{model}</model>
  <files>{files}</files>
  <action>{action}</action>
  <verify>{verify_command}</verify>
  <done>{done_condition}</done>
</task>

아티팩트 경로: {artifact_dir}
```

---

## 에이전트 MD — 트리 구조 로딩

에이전트 소환 시 전체 MD를 읽지 않는다.
반드시 `index.md`만 먼저 읽고, 필요한 세부 파일만 선택적으로 Read한다.
전체 파일을 한번에 읽는 것은 **금지**한다.
