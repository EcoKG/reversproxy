# Vela CLI 레퍼런스

## vela-engine (파이프라인 엔진)

```bash
node .vela/cli/vela-engine.js init "설명" --scale <small|medium|large>
node .vela/cli/vela-engine.js state
node .vela/cli/vela-engine.js transition
node .vela/cli/vela-engine.js record pass|fail
node .vela/cli/vela-engine.js sub-transition
node .vela/cli/vela-engine.js branch [--mode auto|prompt|none]
node .vela/cli/vela-engine.js commit [--message TEXT]
node .vela/cli/vela-engine.js cancel
node .vela/cli/vela-engine.js history
node .vela/cli/vela-engine.js review                             # SDK 3단계 리뷰 (Haiku→Sonnet→Opus)
node .vela/cli/vela-engine.js plan-check                         # SDK plan.md 구조 검증 (Haiku)
node .vela/cli/vela-engine.js research                           # SDK 3-관점 병렬 리서치 (Haiku)
node .vela/cli/vela-engine.js execute                            # SDK 단일 실행 (Sonnet)
```

## vela-read (읽기)

```bash
node .vela/cli/vela-read.js <파일경로>                    # 파일 읽기
node .vela/cli/vela-read.js <파일경로> --lines 50          # 처음 50줄
node .vela/cli/vela-read.js --glob "**/*.ts"               # 패턴 검색
node .vela/cli/vela-read.js --grep "패턴" --ext js,ts      # 내용 검색
node .vela/cli/vela-read.js --tree --depth 3               # 디렉토리 구조
node .vela/cli/vela-read.js --cached                       # 캐시된 경로 조회
```

## vela-write (쓰기)

```bash
node .vela/cli/vela-write.js <파일경로> --content "내용"   # 파일 작성
node .vela/cli/vela-write.js <파일경로> --stdin            # stdin으로 내용 전달
node .vela/cli/vela-write.js <파일경로> --edit --old "원본" --new "수정"
node .vela/cli/vela-write.js --mkdir <디렉토리>            # 디렉토리 생성
```

## TreeNode 캐시

```bash
node .vela/cache/treenode.js ingest     # 대기 경로 SQLite 반영
node .vela/cache/treenode.js query src/ # 접두사로 검색
node .vela/cache/treenode.js stats      # 캐시 통계
node .vela/cache/treenode.js clear      # 캐시 초기화
node .vela/cache/treenode.js export     # 전체 경로 내보내기
```

## 설치 관리

```bash
node .vela/install.js              # 설치 (유효성 검증 포함)
node .vela/install.js verify       # 검증만
node .vela/install.js uninstall    # 완전 제거
node .vela/install.js status       # 현재 상태
node .vela/install.js --json       # JSON 출력
```

## vela-cost (비용/메트릭)

```bash
node .vela/cli/vela-cost.js        # 파이프라인 비용 리포트
```

## vela-report (대시보드)

```bash
node .vela/cli/vela-report.js                    # JSON 리포트
node .vela/cli/vela-report.js --html report.html # HTML 대시보드
```
