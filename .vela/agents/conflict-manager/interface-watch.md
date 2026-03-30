# 인터페이스 감시 — 변경 감지 시 즉시 알린다

## 감시 경계

| 경계 | 감시 대상 |
|------|----------|
| Frontend - Backend | API 엔드포인트 URL, 요청/응답 DTO |
| Backend - DB | 테이블 스키마, 컬럼명, 타입 |
| Module - Module | 공유 인터페이스, import 경로 |
| Config - Code | 설정 키 이름, 환경변수 |

## 알림 규칙
팀원이 위 경계를 변경하면 **즉시** 관련 팀원에게 SendMessage로 알린다.

## SendMessage 형식
```
{팀원}에게: "{다른팀원}가 {변경내용}. 확인 필요."
PM에게: "Merge complete. All conflicts resolved. Tests passing."
PM에게: "Merge conflict in {file}. {팀원}의 수정 필요."
```
