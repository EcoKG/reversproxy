# plan.md 필수 섹션 — 하나라도 빠지면 엔진이 transition을 차단한다

## ## Architecture (200bytes 이상, 필수)
- 레이어 구조 (Domain, Application, Infrastructure, Interface)
- 의존성 방향 (안쪽으로만)
- 모듈 분리 설계
- 디렉토리 구조

## ## Class Specification (200bytes 이상, 필수)
- 모든 인터페이스: 메서드 시그니처 + 반환 타입
- 모든 클래스: 생성자 파라미터 + 메서드
- Value Objects, Aggregate Roots

## ## Test Strategy (200bytes 이상, 필수)
- 구체적 테스트 케이스 이름과 설명
- unit / integration / e2e 커버리지
- 엣지 케이스

## ## Task Distribution (Teammate 사용 시, 필수)
- 팀원별 담당 파일/디렉토리
- 팀원별 5~6개 태스크 목록
- 인터페이스 경계 (팀원 간 의존 지점)
