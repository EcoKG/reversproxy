# Project Roadmap: reversproxy

## Overview
Go 기반 고성능 리버스 터널 프록시 시스템. NAT/방화벽 뒤의 서비스를 외부에서 접근할 수 있도록, 프록시 서버가 클라이언트와 터널을 구성하여 TCP 및 HTTP/HTTPS 트래픽을 중계한다.

## Milestone 1: MVP — 안정적인 리버스 터널 프록시

### Phase 1: Control Plane 연결
- **Goal:** 클라이언트와 프록시 서버 간 제어 채널(control connection)을 수립하여 양방향 통신이 가능한 상태를 만든다.
- **Depends on:** none
- **Requirements:** [REQ-MULTI-01]
- **Success Criteria:**
  - 클라이언트가 프록시 서버에 연결하면 서버 로그에 클라이언트 등록이 확인된다
  - 여러 클라이언트가 동시에 프록시 서버에 접속하며 각각 독립적으로 식별된다
  - 클라이언트 또는 서버를 종료하면 상대측이 연결 해제를 감지한다
- **Status:** completed
- **Plans:** 1

### Phase 2: TCP 터널링
- **Goal:** 외부 사용자가 프록시 서버의 포트에 TCP 연결하면 해당 트래픽이 클라이언트 뒤의 서비스로 전달되어 양방향 데이터 전송이 이루어진다.
- **Depends on:** Phase 1
- **Requirements:** [REQ-TCP-01]
- **Success Criteria:**
  - 외부 사용자가 프록시 서버의 지정 포트에 TCP 연결하면 클라이언트 뒤의 서비스와 통신할 수 있다
  - 터널을 통해 전송한 데이터가 손실이나 변조 없이 양방향으로 전달된다
  - 하나의 클라이언트에 대해 여러 TCP 연결이 동시에 터널링된다
- **Status:** completed
- **Plans:** 1

### Phase 3: HTTP/HTTPS 호스트 기반 라우팅
- **Goal:** HTTP/HTTPS 요청을 호스트 헤더(또는 SNI) 기반으로 올바른 클라이언트 터널로 라우팅한다.
- **Depends on:** Phase 2
- **Requirements:** [REQ-HTTP-01]
- **Success Criteria:**
  - 서로 다른 호스트명으로 들어오는 HTTP 요청이 각각 올바른 클라이언트의 서비스로 전달된다
  - HTTPS 요청도 호스트 기반으로 올바른 터널로 라우팅된다
  - 존재하지 않는 호스트로 요청하면 명확한 에러 응답을 받는다
- **Status:** pending
- **Plans:** 0

### Phase 4: 자동 재연결 및 세션 복구
- **Goal:** 네트워크 불안정 시 클라이언트가 자동으로 재연결하고 기존 터널 설정을 복구하여 서비스 중단을 최소화한다.
- **Depends on:** Phase 2
- **Requirements:** [REQ-CONN-01]
- **Success Criteria:**
  - 네트워크가 일시적으로 끊겼다 복구되면 클라이언트가 자동으로 프록시 서버에 재연결된다
  - 재연결 후 이전에 등록했던 터널이 자동으로 복구되어 외부 사용자가 다시 접근할 수 있다
  - 반복적인 연결 실패 시 재연결 간격이 점진적으로 늘어나 서버에 과부하를 주지 않는다
- **Status:** pending
- **Plans:** 0

### Phase 5: 다중 클라이언트 동시 운용
- **Goal:** 여러 클라이언트가 각자의 터널을 동시에 운용하며 서로 간섭 없이 안정적으로 동작한다.
- **Depends on:** Phase 3, Phase 4
- **Requirements:** [REQ-MULTI-01, REQ-TCP-01, REQ-HTTP-01]
- **Success Criteria:**
  - 다수의 클라이언트가 동시에 서로 다른 TCP 포트와 HTTP 호스트를 등록하여 독립적으로 터널을 운영한다
  - 한 클라이언트의 연결 문제가 다른 클라이언트의 터널에 영향을 주지 않는다
  - 높은 동시 접속 상황에서도 터널 응답 지연이 허용 범위 내에 유지된다
- **Status:** pending
- **Plans:** 0

### Phase 6: 운영 안정성 및 관측성
- **Goal:** 프록시 시스템의 상태를 모니터링하고 문제를 진단할 수 있는 관측 수단을 제공하여 운영 환경에서 안정적으로 사용할 수 있다.
- **Depends on:** Phase 5
- **Requirements:** [REQ-CONN-01, REQ-MULTI-01]
- **Success Criteria:**
  - 현재 연결된 클라이언트 목록과 활성 터널 정보를 조회할 수 있다
  - 터널링 트래픽 통계(연결 수, 전송량)를 확인할 수 있다
  - 비정상 상황(연결 실패, 터널 오류) 발생 시 로그에서 원인을 파악할 수 있다
  - 설정 파일을 통해 서버와 클라이언트의 동작을 구성할 수 있다
- **Status:** pending
- **Plans:** 0

---

## Progress Summary

| Phase | Status | Plans | Completed |
|---|---|---|---|
| Phase 1: Control Plane 연결 | completed | 1 | 11 |
| Phase 2: TCP 터널링 | completed | 1 | 8 |
| Phase 3: HTTP/HTTPS 호스트 기반 라우팅 | pending | 0 | 0 |
| Phase 4: 자동 재연결 및 세션 복구 | pending | 0 | 0 |
| Phase 5: 다중 클라이언트 동시 운용 | pending | 0 | 0 |
| Phase 6: 운영 안정성 및 관측성 | pending | 0 | 0 |

---

## RULES
- Milestone must have >=1 phase
- Phase must have: Goal (1 sentence), Success Criteria (2-5 observable behaviors), Status
- Status values: pending | in_progress | completed | skipped
- Depends on: list of phase numbers (must form a DAG, no cycles)
- Requirements: REQ-IDs from project definition
- Success Criteria must be user-observable behaviors, NOT technical jargon
- Plans count is updated by the planner after planning completes
