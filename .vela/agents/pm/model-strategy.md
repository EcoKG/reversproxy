# 모델 선택 전략 — 반드시 적용

모든 에이전트 소환 시 model 파라미터를 **반드시** 지정한다. 생략 금지.

| 작업 유형 | model 파라미터 | 용도 |
|----------|---------------|------|
| 파일 탐색, 검색, 읽기 | `"haiku"` | Glob/Grep/Read 중심 |
| 코드 구현, 수정, 테스트, 리뷰 | `"sonnet"` | 코딩/리뷰 |
| 설계, 디버깅, 리서치 분석 | `"sonnet"` | 깊은 사고 (에스컬레이션 시 opus) |

## 역할별 기본 모델

| 역할 | model | 비고 |
|------|-------|------|
| Researcher | `"sonnet"` | 에스컬레이션 시 opus |
| Planner | `"sonnet"` | 에스컬레이션 시 opus |
| Executor | `"sonnet"` | 코딩 품질 + 비용 효율 |
| Reviewer | `"sonnet"` | 코드 리뷰 |
| Conflict Manager | `"sonnet"` | 충돌 관리 |
| 탐색 전용 | `"haiku"` | 빠른 파일 탐색 |
�행한 작업이 품질 기준에 미달할 경우, 동일 작업을 Opus로 재소환한다.

### 에스컬레이션 기준
1. **Reviewer 점수 미달**: Reviewer 점수가 15/25 미만
2. **PM reject 연속**: PM이 동일 단계에서 2회 연속 reject

### 에스컬레이션 동작
- Opus 모델로 동일 작업을 재소환한다
- 기존 Sonnet 산출물을 컨텍스트로 전달하여 처음부터 재작업하지 않도록 한다

### 에스컬레이션 기록
- `approval-{step}.json`에 `escalated: true` 표기
- 에스컬레이션 사유(점수 미달 / reject 연속)를 함께 기록
