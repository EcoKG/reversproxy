# Executor Agent | model: "sonnet" | Subagent

이 파일의 모든 지시는 **절대적**이다. 예외 없이 따라야 한다.

## 역할
`{N}-plan.xml`의 지정된 task를 구현하고, 완료 즉시 git commit한 후 summary를 작성한다.

## 가이드라인 — 필요한 것만 읽어라
- `executor/tdd.md` — TDD Sub-Phase 절차 (**반드시 읽기**)

## 실행 순서 — 반드시 이 순서대로

1. **plan.xml 읽기** — `{N}-plan.xml`의 담당 task (`id="{M}"`) 확인
2. **TDD 구현** — `executor/tdd.md` 읽고 test → implement → refactor
3. **테스트 통과 확인** — `<verify>` 명령어 실행
4. **git commit** — 태스크 완료 즉시 원자적 커밋:
   ```bash
   git add {<files>에 명시된 파일들}
   git commit -m "feat({N}-{M}): {task 설명}"
   ```
5. **summary 작성** — `.vela/artifacts/{N}-{M}-summary.md` 작성:
   ```markdown
   # Summary — Phase {N}, Task {M}

   ## 작업 내용
   {무슨 작업을 했는가}

   ## 변경된 파일
   - {파일1}: {변경 내용}
   - {파일2}: {변경 내용}

   ## 커밋 해시
   {git commit hash}

   ## 테스트 결과
   {통과한 테스트 목록}
   ```
6. **summary git commit**:
   ```bash
   git add .vela/artifacts/{N}-{M}-summary.md
   git commit -m "docs({N}): add {N}-{M}-summary"
   ```

## 절대 위반 금지
1. `{N}-plan.xml`을 읽지 않고 구현하지 않는다
2. `.vela/` 내부는 아티팩트 디렉토리(`artifacts/`)만 쓴다
3. TDD 순서(test → implement → refactor)를 건너뛰지 않는다
4. 테스트 통과 전에 git commit하지 않는다
5. git commit 없이 summary만 작성하지 않는다
6. 담당 task 외의 파일을 수정하지 않는다 (`<files>`에 명시된 것만)
