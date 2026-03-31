#!/bin/bash
# ⛵ Vela Status Line — shows pipeline state in Claude Code's bottom bar
# Receives JSON session data via stdin from Claude Code

input=$(cat)

MODEL=$(echo "$input" | jq -r '.model.display_name // "unknown"' 2>/dev/null)
PCT=$(echo "$input" | jq -r '.context_window.used_percentage // 0' 2>/dev/null | cut -d. -f1)
CWD=$(echo "$input" | jq -r '.workspace.current_dir // "."' 2>/dev/null)

# Find .vela directory
VELA_DIR=""
if [ -d "$CWD/.vela" ]; then
  VELA_DIR="$CWD/.vela"
elif [ -d ".vela" ]; then
  VELA_DIR=".vela"
fi

if [ -z "$VELA_DIR" ]; then
  echo "$MODEL | ${PCT}% ctx"
  exit 0
fi

# Find active pipeline
PIPELINE_STATE="none"
CURRENT_STEP=""
PIPELINE_TYPE=""
REQUEST=""
STEP_INDEX=0
TOTAL_STEPS=0

if [ -d "$VELA_DIR/artifacts" ]; then
  STATE_FILE=$(find "$VELA_DIR/artifacts" -name "pipeline-state.json" -type f 2>/dev/null | \
    xargs ls -t 2>/dev/null | head -1)

  if [ -n "$STATE_FILE" ]; then
    STATUS=$(jq -r '.status // "unknown"' "$STATE_FILE" 2>/dev/null)
    if [ "$STATUS" = "active" ]; then
      PIPELINE_STATE="active"
      CURRENT_STEP=$(jq -r '.current_step // "?"' "$STATE_FILE" 2>/dev/null)
      PIPELINE_TYPE=$(jq -r '.pipeline_type // "?"' "$STATE_FILE" 2>/dev/null)
      REQUEST=$(jq -r '.request // ""' "$STATE_FILE" 2>/dev/null | cut -c1-25)
      STEP_INDEX=$(jq -r '.current_step_index // 0' "$STATE_FILE" 2>/dev/null)
      TOTAL_STEPS=$(jq -r '.steps | length // 0' "$STATE_FILE" 2>/dev/null)
    fi
  fi
fi

# Build progress bar
progress_bar() {
  local current=$1
  local total=$2
  local width=8
  if [ "$total" -gt 0 ]; then
    local filled=$(( (current * width) / total ))
    local empty=$(( width - filled ))
    printf "["
    for i in $(seq 1 $filled); do printf "="; done
    if [ $filled -lt $width ]; then printf ">"; empty=$((empty - 1)); fi
    for i in $(seq 1 $empty); do printf "-"; done
    printf "] %d/%d" "$((current + 1))" "$total"
  fi
}

# Build status line
if [ "$PIPELINE_STATE" = "active" ]; then
  PROGRESS=$(progress_bar "$STEP_INDEX" "$TOTAL_STEPS")
  echo -e "⛵ Vela ✦ \033[32m${PIPELINE_TYPE}\033[0m 🧭 ${CURRENT_STEP} ${PROGRESS} │ ${REQUEST}… │ ${PCT}%"
else
  echo -e "⛵ Vela ✦ Explore │ $MODEL ${PCT}%"
fi
