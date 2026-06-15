#!/usr/bin/env bash
# Fetch provider model data from models.dev API and generate provider_models.json.
#
# Replaces scripts/generate-provider-models.py — pure shell + curl + jq,
# no Python dependency.
#
# Usage:
#   scripts/fetch-provider-models.sh [--output PATH]
#
# Without --output, writes to stdout.

set -euo pipefail

API_URL="https://models.dev/api.json"
OUTPUT=""

# Parse arguments
for arg in "$@"; do
    case "$arg" in
        --output=*) OUTPUT="${arg#--output=}" ;;
        --output)
            shift
            OUTPUT="$1"
            ;;
    esac
done

# Require jq
if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required but not found" >&2
    exit 1
fi

# Fetch API data
echo "Fetching $API_URL ..." >&2
API_DATA=$(curl -sSf -H "User-Agent: clawbench-fetch-models/1.0" --connect-timeout 15 --max-time 60 "$API_URL")

if [ -z "$API_DATA" ]; then
    echo "ERROR: Failed to fetch models.dev API" >&2
    exit 1
fi

# Build the entire JSON in a single jq invocation.
# Store input as $api first, then iterate over provider mappings.
RESULT=$(echo "$API_DATA" | jq '
  . as $api |

  # Provider mapping: [clawbench_id, modelsdev_id, extra_filter_or_null]
  [
    ["openai", "openai", null],
    ["anthropic", "anthropic", null],
    ["google", "google", null],
    ["deepseek", "deepseek", null],
    ["groq", "groq", null],
    ["openrouter", "openrouter", null],
    ["cerebras", "cerebras", null],
    ["xai", "xai", null],
    ["mistral", "mistral", null],
    ["fireworks", "fireworks-ai", null],
    ["minimax", "minimax", null],
    ["minimax-cn", "minimax-cn", null],
    ["kimi-coding", "kimi-for-coding", null],
    ["moonshotai", "moonshotai", null],
    ["moonshotai-cn", "moonshotai-cn", null],
    ["xiaomi", "xiaomi", null],
    ["xiaomi-token-plan-cn", "xiaomi-token-plan-cn", null],
    ["xiaomi-token-plan-ams", "xiaomi-token-plan-ams", null],
    ["xiaomi-token-plan-sgp", "xiaomi-token-plan-sgp", null],
    ["zai", "zai-coding-plan", null],
    ["huggingface", "huggingface", null],
    ["opencode", "opencode", null],
    ["vercel-ai-gateway", "vercel", "vercel"]
  ] | map(
    . as $p |
    # Get models for this provider from the API data
    (if ($api[$p[1]].models // null) == null then []
     else
       [$api[$p[1]].models | to_entries[]
         | select(.value.tool_call == true)
         # Apply extra filter for vercel: only anthropic/ prefix
         | select(
             if $p[2] == "vercel" then (.value.id | startswith("anthropic/"))
             else true end
           )
         | {
             id: .value.id,
             name: (.value.name // .value.id),
             context_length: (.value.limit.context // 0),
             max_output_tokens: (.value.limit.output // 0),
             supports_thinking: (
               (.value.reasoning // false) |
               if type == "boolean" then .
               else (. != null and . != false and . != 0) end
             ),
             cost_tier: (
               if (.value.cost.output // 0) == 0 then "cheap"
               elif (.value.cost.output | . <= 1) then "cheap"
               elif (.value.cost.output | . <= 10) then "moderate"
               else "expensive" end
             )
           }
       ] | sort_by(.id)
     end) as $models |
    # Only include providers with at least one model
    if ($models | length) > 0 then {key: $p[0], value: {models: $models}}
    else empty end
  ) | from_entries |

  {
    _generated_at: (now | todate),
    _source: "https://models.dev/api.json",
    providers: .
  }
')

# Print per-provider stats
echo "$RESULT" | jq -r '.providers | to_entries[] | "  \(.key): \(.value.models | length) models"' >&2

TOTAL_MODELS=$(echo "$RESULT" | jq '[.providers[].models | length] | add')
TOTAL_PROVIDERS=$(echo "$RESULT" | jq '.providers | length')
echo "" >&2
echo "Wrote $TOTAL_MODELS models across $TOTAL_PROVIDERS providers" >&2

if [ -n "$OUTPUT" ]; then
    mkdir -p "$(dirname "$OUTPUT")"
    echo "$RESULT" > "$OUTPUT"
    echo "Output: $OUTPUT" >&2
else
    echo "$RESULT"
fi
