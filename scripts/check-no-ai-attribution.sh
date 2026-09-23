#!/usr/bin/env bash
# Reject AI-assistant attribution in commit messages: AI-assisted work is
# welcome, but credited authorship is the person who opens the PR, and a
# squash merge carries the PR title/body plus commit messages verbatim
# onto main.
#
# Used from two places:
#   - .githooks/commit-msg      (local commits, opt-in via core.hooksPath)
#   - .github/workflows/ci.yml  (PR + push messages — the merge gate)
#
# Usage: check-no-ai-attribution.sh [file ...]
#   With no args, reads stdin. Input is raw commit-message text; multiple
#   concatenated messages (git log --format=%B) are fine — checks are
#   line-based. Exit 0 = clean, exit 1 = violation found.
set -u

# Attribution phrases that must not be paired with an AI tool name.
ATTR='(co[-_ ]?authored[-_ ]?by|authored[-_ ]?by|generated[-_ ]?(with|using|by)|co[-_ ]?created|assisted[-_ ]?by|written[-_ ]?with|pair(ed)?[-_ ]?with|ai[-_ ]?assistant)'
# Known AI coding tools/agents (lowercase; matched case-insensitively).
TOOL='(claude|anthropic|openai|chatgpt|codex|copilot|gemini|deepseek|glm|zhipu|z\.ai|qwen|kimi|moonshot|grok|mistral|cursor|windsurf|codeium|devin|jules|aider|cline|trae|qoder|opencode|augmentcode)'

is_bad_line() {
  local line="$1"
  case "$line" in
    *🤖*) return 0 ;;
  esac
  if grep -qiE -- "$ATTR" <<< "$line" && grep -qiE -- "$TOOL" <<< "$line"; then
    return 0
  fi
  return 1
}

status=0
lineno=0
src=""

report() {
  echo "ERROR: AI attribution found in commit message — this is banned in" >&2
  echo "this repository (see CONTRIBUTING.md 'Commit / PR conventions')." >&2
  echo "Source: $src, line $lineno:" >&2
  echo "    $1" >&2
  echo "Strip the attribution lines (e.g. amend / edit the message) and retry." >&2
}

check_stream() {
  while IFS= read -r line || [ -n "$line" ]; do
    lineno=$((lineno + 1))
    if is_bad_line "$line"; then
      report "$line"
      status=1
    fi
  done
}

if [ "$#" -eq 0 ]; then
  src="<stdin>"
  check_stream
else
  for f in "$@"; do
    if [ ! -r "$f" ]; then
      echo "ERROR: cannot read input file: $f" >&2
      exit 1
    fi
    src="$f"
    lineno=0
    check_stream < "$f"
  done
fi

exit "$status"
