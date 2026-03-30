# 팀 구성 및 에이전트 위임 규칙 — 절대 준수

## Teammate vs Subagent — 예외 없이 구분

**Teammate** = 에이전트 간 SendMessage 소통이 필요한 작업. 반드시 TeamCreate 후 team_name으로 소환.
**Subagent** = 독립 단일 작업. 소통 불필요. team_name 없이 소환.

| 조건 | 방식 | model |
|------|------|-------|
| 경쟁가설 디버깅 (리서치) | **Subagent** | `"sonnet"` |
| 다중 파일/모듈 동시 수정 | **Teammate** | `"sonnet"` |
| CrossLayer 개발 | **Teammate** | `"sonnet"` |
| 독립 리뷰/점검 | **Subagent** | `"sonnet"` |
| 단일 파일/모듈 수정 | **Subagent** | `"sonnet"` |
| 파일 탐색/검색 | **Subagent** | `"haiku"` |
| 설계/디버깅 분석 | **Subagent** | `"sonnet"` |
| 에스컬레이션 재시도 | **Subagent** | `"opus"` |

## 팀 규모 — 반드시 준수
- 팀 크기: **3~5명** (개발 팀원 + Conflict Manager 1명)
- 태스크 배분: **팀원당 5~6개**
- 파일 소유권: 각 팀원에게 담당 파일을 **반드시 명시적으로** 부여
- 동일 파일을 여러 팀원이 수정하는 것은 **절대 금지**

## 컨텍스트 Tier 로딩 — 에이전트에 필요한 것만 전달

에이전트 소환 시 **과도한 컨텍스트를 주지 않는다**. 단계별로 필수 컨텍스트만 전달한다.

| Tier | 포함 | 언제 |
|------|------|------|
| 0 (항상) | 아티팩트 경로, 현재 단계 | 모든 소환 |
| 1 (리서치) | 프로젝트 설명 (request), 기존 분석 결과 | research 단계 |
| 2 (계획) | research.md 경로, 프로젝트 구조 요약 | plan 단계 |
| 3 (실행) | plan.md 경로, 담당 파일, 태스크 목록 | execute 단계 |

**절대 하지 않을 것**: 전체 소스 코드를 프롬프트에 포함. 이전 단계의 모든 산출물을 한꺼번에 전달.

## XML 구조화 태스크 — 에이전트 소환 시 반드시 사용

에이전트에게 태스크를 전달할 때 **반드시 XML 구조**를 사용한다.
자연어만으로 전달하는 것은 **금지**한다.

```xml
<task>
  <role>executor</role>
  <model>sonnet</model>
  <files>src/api/auth.js, src/middleware/jwt.js</files>
  <action>JWT 검증 미들웨어를 구현한다. plan.md의 Class Specification을 따른다.</action>
  <verify>npm test -- --grep "auth"</verify>
  <done>모든 인증 테스트가 통과하고, review-execute.md가 작성되었다.</done>
</task>
```

### XML 필드 설명
- `<role>`: 에이전트 역할 (executor, researcher, planner 등)
- `<model>`: 사용할 모델 (반드시 지정)
- `<files>`: 대상 파일 (Teammate의 경우 담당 파일)
- `<action>`: 구체적 작업 지시
- `<verify>`: 검증 방법 (실행 가능한 명령어)
- `<done>`: 완료 조건 (명확하고 검증 가능)

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

## 에이전트 MD — 트리 구조 로딩
에이전트 소환 시 전체 MD를 읽지 않는다.
반드시 `index.md`만 먼저 읽고, 필요한 세부 파일만 선택적으로 Read한다.
전체 파일을 한번에 읽는 것은 **금지**한다.
