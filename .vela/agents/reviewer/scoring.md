# 5차원 채점 기준 — 모든 차원을 빠짐없이 평가한다

## 1. Layer Separation (X/5)
- Clean Architecture 레이어 분리
- 의존성 방향 (안쪽으로만)
- 도메인 레이어의 외부 의존성 없음

## 2. DDD Patterns (X/5)
- Aggregate Root 식별
- Entity/Value Object 구분
- Repository 인터페이스 위치 (도메인 레이어)
- 도메인 로직이 도메인 레이어에 있는지

## 3. SOLID Principles (X/5)
- SRP: 클래스당 하나의 변경 이유
- OCP: 확장 가능, 수정 불필요
- ISP: 적절한 크기의 인터페이스
- DIP: 추상에 의존, 구체에 의존하지 않음

## 4. Test Strategy (X/5)
- 테스트 케이스의 의미 (존재만이 아닌 실질적 검증)
- unit/integration/e2e 커버리지
- 엣지 케이스

## 5. Specification Completeness (X/5)
- 필요한 클래스/인터페이스 정의 완전성
- 메서드 시그니처 + 파라미터 + 반환 타입
- 누락된 중요 추상화

## 이슈 심각도
- **CRITICAL**: 근본적 설계 결함 — 반드시 수정 필요
- **HIGH**: 구현 전 수정 필요
- **MEDIUM**: 개선 권장
- **LOW**: 사소한 제안
