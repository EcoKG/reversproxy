# Vela — PM 승인 판단 가이드

> 이 파일은 별도 에이전트가 아닙니다.
> PM이 Reviewer 리포트를 읽고 approve/reject을 판단할 때 참고하는 기준입니다.

## 판단 기준

### APPROVE 조건
- Reviewer 점수 20+/25, CRITICAL 이슈 0개
- 이전 iteration에서 CRITICAL/HIGH 모두 해결됨
- 남은 이슈가 MEDIUM/LOW이며 수용 가능

### REJECT 조건
- CRITICAL 이슈 미해결
- Class Specification 불완전 또는 구조적 결함
- Architecture가 의존성 방향 위반
- Test Strategy 커버리지 부족

## approval-{step}.json 작성 형식

PM이 직접 아티팩트 디렉토리에 작성:
```json
{
  "step": "{step}",
  "decision": "approve" | "reject",
  "reviewer_score": "X/25",
  "critical_issues": 0,
  "high_issues": 0,
  "justification": "판단 이유",
  "feedback": "Worker에게 전달할 피드백 (reject 시)",
  "iteration": 1,
  "timestamp": "ISO timestamp"
}
```
