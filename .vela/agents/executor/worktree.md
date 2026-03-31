# Git Worktree 격리 실행

`isolation: "worktree"`로 소환된 경우:

## 규칙
1. 격리된 git worktree에서 작업 중이다. 다른 팀원과 파일 시스템이 분리되어 있다
2. 작업 완료 후 Conflict Manager가 병합한다
3. 인터페이스 변경 시 관련 팀원에게 **반드시 SendMessage**로 알린다
