# Vela 인터랙티브 UI 레퍼런스

이 파일은 `vela.md`에서 분리된 AskUserQuestion JSON 템플릿입니다.
PM이 해당 단계에 진입할 때 이 파일의 해당 섹션을 읽으세요.

---

## 프롬프트 최적화 — 보완 항목 선택

```json
{
  "questions": [{
    "question": "프롬프트를 확인합니다. 보완이 필요하면 선택해주세요.",
    "header": "⛵ Prompt",
    "options": [
      { "label": "이대로 진행 (Recommended)", "description": "현재 프롬프트가 충분합니다. 바로 시작합니다." },
      { "label": "대상 파일/모듈 지정", "description": "수정할 파일이나 모듈을 구체적으로 지정합니다." },
      { "label": "범위 좁히기", "description": "전체가 아닌 특정 기능/클래스/메서드로 범위를 좁힙니다." },
      { "label": "문제 상세 설명", "description": "버그 재현 조건, 에러 메시지, 기대 동작을 추가합니다." }
    ],
    "multiSelect": false
  }]
}
```

## 프롬프트 최적화 — 조립 후 확인

```json
{
  "questions": [{
    "question": "위 프롬프트로 진행할까요?",
    "header": "⛵ 확인",
    "options": [
      { "label": "이대로 진행 (Recommended)", "description": "최적화된 프롬프트로 파이프라인 시작" },
      { "label": "추가 보완", "description": "더 구체적인 정보 추가" },
      { "label": "원본으로 진행", "description": "최적화 없이 원래 요청 그대로 진행" },
      { "label": "취소", "description": "요청 취소" }
    ],
    "multiSelect": false
  }]
}
```

## 세션 재개

```json
{
  "questions": [{
    "question": "이전 세션에서 중단된 파이프라인이 있습니다. 어떻게 할까요?",
    "header": "⛵ Resume",
    "options": [
      { "label": "재개 (Recommended)", "description": "중단된 지점부터 파이프라인을 계속합니다." },
      { "label": "취소하고 새로 시작", "description": "이전 파이프라인을 취소하고 새 작업을 시작합니다." },
      { "label": "무시", "description": "이전 파이프라인을 그대로 두고 Explore 모드로 진입합니다." }
    ],
    "multiSelect": false
  }]
}
```

## Research 방식 선택 (Explore에서)

```json
{
  "questions": [{
    "question": "어떤 방식으로 분석을 진행할까요?",
    "header": "🔭 Research",
    "options": [
      { "label": "Solo (직접 분석) (Recommended)", "description": "Vela가 직접 분석. 가장 빠르고 비용 없음." },
      { "label": "Subagent (독립 리서처)", "description": "독립 컨텍스트에서 집중 분석. 편향 없는 리포트." },
      { "label": "Teammate 3명 병렬 (Opus)", "description": "경쟁가설 디버깅. 3명이 서로 가설을 반박/검증. 가장 철저." }
    ],
    "multiSelect": false
  }]
}
```

## 파이프라인 규모 선택

```json
{
  "questions": [{
    "question": "파이프라인 규모를 선택해주세요.",
    "header": "🧭 Scale",
    "options": [
      { "label": "Small (trivial)", "description": "init → execute → commit → finalize. 단일 파일, 10줄 이하 수정." },
      { "label": "Medium (quick)", "description": "init → plan → execute → verify → commit → finalize. 3파일 이하." },
      { "label": "Large (standard)", "description": "전체 10단계 + Teammate 리서치(경쟁가설) + CrossLayer Teammate. 대규모 작업." }
    ],
    "multiSelect": false
  }]
}
```

## Checkpoint (사용자 승인)

```json
{
  "questions": [{
    "question": "구현 계획을 검토했습니다. 어떻게 진행할까요?",
    "header": "✦ Checkpoint",
    "options": [
      { "label": "승인 (Recommended)", "description": "이 계획대로 구현을 진행합니다." },
      { "label": "변경 요청", "description": "계획에 수정이 필요합니다. 피드백을 입력합니다." },
      { "label": "파이프라인 취소", "description": "이 작업을 중단합니다." }
    ],
    "multiSelect": false
  }]
}
```

## Commit 메시지 확인

```json
{
  "questions": [{
    "question": "커밋 메시지를 확인해주세요.",
    "header": "⚓ Commit",
    "options": [
      { "label": "이 메시지로 커밋 (Recommended)", "description": "자동 생성된 conventional commit 메시지를 사용합니다." },
      { "label": "메시지 수정", "description": "직접 커밋 메시지를 작성합니다." },
      { "label": "diff 먼저 확인", "description": "변경사항을 확인한 후 커밋합니다." }
    ],
    "multiSelect": false
  }]
}
```

## Finalize (PR 생성)

```json
{
  "questions": [{
    "question": "파이프라인이 완료되었습니다. PR을 생성할까요?",
    "header": "⛵ PR",
    "options": [
      { "label": "PR 생성", "description": "feature 브랜치에서 base 브랜치로 Pull Request를 생성합니다." },
      { "label": "PR 생성하지 않음 (Recommended)", "description": "커밋만 남기고 PR은 나중에 수동으로 생성합니다." }
    ],
    "multiSelect": false
  }]
}
```

## Cancel 확인

```json
{
  "questions": [{
    "question": "파이프라인을 정말 취소할까요?",
    "header": "⚠ Cancel",
    "options": [
      { "label": "취소 진행", "description": "파이프라인을 취소합니다. 변경사항은 유지되며 복구 안내가 제공됩니다." },
      { "label": "계속 진행 (Recommended)", "description": "파이프라인을 계속 진행합니다." }
    ],
    "multiSelect": false
  }]
}
```

## PM 거부 시 사용자 알림

```json
{
  "questions": [{
    "question": "Reviewer가 이슈를 발견했습니다. 어떻게 할까요?",
    "header": "🌟 Review",
    "options": [
      { "label": "자동 수정 (Recommended)", "description": "Reviewer 피드백을 반영하여 Worker를 재소환합니다." },
      { "label": "직접 가이드", "description": "수정 방향에 대해 직접 지시합니다." },
      { "label": "무시하고 승인", "description": "이슈를 수용하고 이대로 진행합니다." },
      { "label": "파이프라인 취소", "description": "이 작업을 중단합니다." }
    ],
    "multiSelect": false
  }]
}
```
