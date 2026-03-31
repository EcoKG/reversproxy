# 차단 시 복구 — 같은 행동을 절대 재시도하지 않는다

훅이 `BLOCKED [코드]` 메시지를 반환하면, 아래 테이블에 따라 **즉시** 행동을 전환한다.

## Gate Keeper (VK-*)

| 코드 | 사유 | 복구 |
|------|------|------|
| VK-01 | Bash 쓰기 (읽기 모드) | Read/Glob/Grep 또는 .vela/cli/vela-read.js |
| VK-02 | Bash 제한 | Claude Code 내장 도구. git/gh는 파이프라인 활성 시 허용 |
| VK-03 | pipeline-state.json 직접 수정 | `vela-engine transition` |
| VK-04 | 읽기 모드에서 쓰기 | `vela-engine transition` → 쓰기 가능 단계 |
| VK-05 | 민감 파일 | .env.example 사용 |
| VK-06 | 시크릿 감지 | 환경변수로 대체 |
| VK-07 | PM 소스코드 직접 접근 | Agent 도구로 에이전트 소환하여 위임 |

## Gate Guard (VG-*)

| 코드 | 사유 | 복구 |
|------|------|------|
| VG-EXPLORE | Explore에서 쓰기 | /vela start |
| VG-00 | TaskCreate | 파이프라인 단계를 따른다 |
| VG-01 | research 없이 plan | research 먼저 |
| VG-02 | execute 전 소스 수정 | transition → execute |
| VG-03 | 테스트 실패 commit | 테스트 수정 후 재실행 |
| VG-04 | verification 없이 report | verification 먼저 |
| VG-05 | pipeline-state.json | vela-engine transition |
| VG-06 | 리비전 한도 | transition 또는 사용자 승인 |
| VG-07 | 잘못된 단계 git commit | commit 단계에서 실행 |
| VG-08 | verify 전 push | verify 완료 후 |
| VG-11 | 비-team 단계 approval | team 단계에서 작성 |
| VG-12 | PM 직접 소스 수정 | Subagent/Teammate 소환 |

## 원칙
1. **절대 재시도 금지** — 같은 도구+같은 입력은 같은 차단
2. **Recovery 메시지를 따른다**
3. **단계를 건너뛰지 않는다**
4. **복구 불가능 시 AskUserQuestion으로 사용자에게 알린다**
