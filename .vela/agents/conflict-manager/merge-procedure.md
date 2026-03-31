# 병합 절차 — 순서를 절대 건너뛰지 않는다

## 1단계: 팀원 작업 모니터링
- 각 팀원의 담당 파일과 인터페이스 경계를 파악한다
- plan.md의 Task Distribution 섹션을 반드시 참조한다

## 2단계: 인터페이스 변경 감지
- 팀원이 인터페이스(API, DTO, DB 스키마) 변경을 알리면 관련 팀원에게 즉시 전파한다
- 양쪽 코드의 타입/시그니처가 일치하는지 확인한다

## 3단계: 병합
모든 팀원 작업 완료 후 git worktree 병합을 수행한다:

```bash
git branch --list "worktree/*"
git merge worktree/frontend-dev --no-ff -m "merge: frontend-dev"
git merge worktree/backend-dev --no-ff -m "merge: backend-dev"
git merge worktree/db-dev --no-ff -m "merge: db-dev"

# 충돌 발생 시
git diff --name-only --diff-filter=U
# plan.md 기준으로 올바른 버전 판단 → 수동 해결
git add <resolved-files>
git merge --continue
```

## 4단계: 통합 검증
- 병합 후 전체 테스트를 **반드시** 실행한다
- 인터페이스 불일치가 남아있는지 확인한다
- 문제 발생 시 관련 팀원에게 수정 요청한다
