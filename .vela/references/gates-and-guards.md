# Gate Keeper & Gate Guard 상세

## Gate Keeper (수문장) — PreToolUse

모든 도구 호출 전에 실행되어 R/W 모드를 강제한다.

### 게이트 규칙

| 게이트 | 코드 | 규칙 | 동작 |
|--------|------|------|------|
| GATE 1 | VK-01, VK-02 | Bash 차단 | Vela CLI 명령 외 Bash 사용 차단. 안전한 읽기 명령(ls, git status 등)은 모든 모드에서 허용. 활성 파이프라인 시 git/gh 명령 허용 (Gate Guard가 단계별 제한) |
| GATE 2 | VK-03, VK-04 | 모드 강제 | 읽기전용 모드에서 Write/Edit 차단. `.vela/` 내부 파일은 예외 (pipeline-state.json 제외) |
| GATE 3 | VK-05 | 민감파일 보호 | .env, credentials.json 등 민감 파일 쓰기 차단 |
| GATE 4 | VK-06 | 시크릿 감지 | API 키, 토큰, 비밀키 등 15개 패턴 감지 시 쓰기 차단 |
| GATE 5 | — | 경로 경고 | node_modules 등 제외 경로 쓰기 시 경고 (차단 아님) |

### 모드별 허용 도구

| 모드 | 허용 | 차단 |
|------|------|------|
| read | Read, Glob, Grep, Agent | Edit, Write, NotebookEdit, Bash(쓰기) |
| write | Read, Write, Edit, NotebookEdit, Glob, Grep | Bash |
| readwrite | Read, Write, Edit, NotebookEdit, Glob, Grep, Agent | Bash(제한적) |

## Gate Guard (가이드라인) — PreToolUse

파이프라인 순서를 강제한다. 무시, 우회, 변형 불가.

### 가드 규칙

| 가드 | 코드 | 규칙 |
|------|------|------|
| GUARD 0 | VG-00 | 파이프라인 중 TaskCreate/TaskUpdate/TaskList 차단 |
| GUARD 0.5 | — | 비-research 단계에서 5회 이상 Read 경고 (차단 아님) |
| GUARD 1 | VG-01 | research.md 없이 plan.md 작성 불가 |
| GUARD 2 | VG-02 | execute 단계 전 소스코드 수정 불가 + pipeline-state.json 보호 |
| GUARD 3 | VG-03 | 빌드/테스트 실패 시 git commit 불가 |
| GUARD 4 | VG-04 | verification.md 없이 report.md 작성 불가 |
| GUARD 5 | VG-05 | pipeline-state.json 직접 수정 불가 |
| GUARD 6 | VG-06 | 단계별 리비전 한도 초과 시 차단 |
| GUARD 7 | VG-07 | execute/commit/finalize에서만 git commit 허용 |
| GUARD 8 | VG-08 | verify 완료 전 git push 차단 |
| GUARD 9 | — | 보호 브랜치 직접 커밋 경고 (차단 아님) |
| GUARD 11 | VG-11 | 비-team 단계에서 approval-*.json / review-*.md 작성 차단 (team 단계에서만 허용) |
| GUARD 12 | VG-12 | execute 단계에서 PM 직접 소스 수정 차단 — SubAgent/Teammate 위임 강제 (delegation.json 기반) |

### Permission Deny 규칙 (절대 차단)

- `rm -rf`, `rm -r`, `git push --force`, `git reset --hard`, `git commit --no-verify`, `git clean -f`
