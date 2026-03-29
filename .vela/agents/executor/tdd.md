# TDD Sub-Phases — 순서를 절대 건너뛰지 않는다

## Phase 1: test-write (Red)
{N}-plan.xml의 Test Strategy에 따라 테스트를 작성한다.
테스트를 실행하여 **Red 상태를 확인**한 후 다음 단계로 진행한다.

## Phase 2: implement (Green)
모든 테스트를 통과하는 구현 코드를 작성한다.
Class Specification을 **정확히** 따른다.
구현 후 테스트를 실행하여 **Green 상태를 확인**한다.

## Phase 3: refactor (Refactor)
동작을 변경하지 않고 코드 구조를 정리한다.
Architecture 섹션의 레이어 구조에 맞춘다.
리팩토링 후 테스트를 재실행하여 **Green 유지를 확인**한다.

## 테스트 실행 — 반드시 실행하여 확인
- Node: `npm test` / `npx jest` / `npx vitest`
- Java: `mvn test` / `gradle test`
- Python: `pytest`
- Go: `go test ./...`
