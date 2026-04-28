#!/usr/bin/env bash
# 用 curl 调用 Responses API 模拟两轮会话，对比「总上下文」(input+output+cache+reasoning) 是否随轮次增长。
# 用法: API_KEY=sk-xxx [BASE_URL=https://gpt.qt.cool] [MODEL=gpt-5.2-codex] ./scripts/test_responses_usage.sh
# 可选: DRY_RUN=1 只打印 curl 命令；DEBUG=1 打印第二轮 response.completed 原始 JSON

set -e
API_KEY="${API_KEY:-$OPENAI_API_KEY}"
BASE_URL="${BASE_URL:-https://gpt.qt.cool}"
URL="${BASE_URL}/v1/responses"
MODEL="${MODEL:-gpt-5.2-codex}"

if [[ -n "${DRY_RUN}" ]]; then
  echo "=== DRY_RUN: 以下为两轮请求的 curl 命令，复制后替换 YOUR_API_KEY 执行，对比响应中 usage.input_tokens ==="
  echo ""
  echo "第一轮 (input: system + user):"
  echo "curl -sS -N -X POST \"$URL\" \\"
  echo "  -H 'Content-Type: application/json' -H 'Authorization: Bearer YOUR_API_KEY' -H 'OpenAI-Beta: responses=v1' \\"
  echo "  -d '{\"model\":\"$MODEL\",\"stream\":true,\"input\":[{\"type\":\"message\",\"role\":\"system\",\"content\":[{\"type\":\"input_text\",\"text\":\"You are a helpful assistant.\"}]},{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"Say hi in one word.\"}]}]}'"
  echo ""
  echo "第二轮 (input: system + user + assistant + user):"
  echo "curl -sS -N -X POST \"$URL\" \\"
  echo "  -H 'Content-Type: application/json' -H 'Authorization: Bearer YOUR_API_KEY' -H 'OpenAI-Beta: responses=v1' \\"
  echo "  -d '{\"model\":\"$MODEL\",\"stream\":true,\"input\":[{\"type\":\"message\",\"role\":\"system\",\"content\":[{\"type\":\"input_text\",\"text\":\"You are a helpful assistant.\"}]},{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"Say hi in one word.\"}]},{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hi.\"}]},{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"What is 1+1? One short sentence.\"}]}]}'"
  echo ""
  echo "从 SSE 流中找到 event: response.completed 对应的 data: 行，response.usage 含 input_tokens、output_tokens、cached_tokens、reasoning_tokens 等；总上下文 = 上述之和。"
  exit 0
fi

if [[ -z "$API_KEY" ]]; then
  echo "请设置 API_KEY 或 OPENAI_API_KEY 后重试。或使用 DRY_RUN=1 仅打印 curl 命令。"
  echo "示例: API_KEY=sk-xxx ./scripts/test_responses_usage.sh"
  exit 1
fi

# 第一轮：system + user
BODY1='{
  "model": "'"$MODEL"'",
  "stream": true,
  "input": [
    {"type": "message", "role": "system", "content": [{"type": "input_text", "text": "You are a helpful assistant."}]},
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Say hi in one word."}]}
  ]
}'

# 第二轮：system + user + assistant + user（模拟多轮后再次请求）
BODY2='{
  "model": "'"$MODEL"'",
  "stream": true,
  "input": [
    {"type": "message", "role": "system", "content": [{"type": "input_text", "text": "You are a helpful assistant."}]},
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Say hi in one word."}]},
    {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "Hi."}]},
    {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "What is 1+1? One short sentence."}]}
  ]
}'

# 从 SSE 流中取含 usage 的最后一根 data 行，解析 input/output/cache/reasoning 并求总上下文
get_total_context() {
  local line
  line=$(grep '^data: ' | grep 'usage' | tail -1)
  [[ -z "$line" ]] && echo "0" && return
  line="${line#data: }"
  local input output cached reasoning total
  input=$(echo "$line" | grep -o '"input_tokens":[0-9]*' | head -1 | cut -d: -f2)
  output=$(echo "$line" | grep -o '"output_tokens":[0-9]*' | head -1 | cut -d: -f2)
  cached=$(echo "$line" | grep -o '"cached_tokens":[0-9]*' | head -1 | cut -d: -f2)
  reasoning=$(echo "$line" | grep -o '"reasoning_tokens":[0-9]*' | head -1 | cut -d: -f2)
  input=${input:-0}
  output=${output:-0}
  cached=${cached:-0}
  reasoning=${reasoning:-0}
  total=$((input + output + cached + reasoning))
  echo "$total"
}

# 同时输出明细（input, output, cache, reasoning）便于核对
get_usage_detail() {
  local line
  line=$(grep '^data: ' | grep 'usage' | tail -1)
  [[ -z "$line" ]] && echo "input=0 output=0 cache=0 reasoning=0" && return
  line="${line#data: }"
  local i o c r
  i=$(echo "$line" | grep -o '"input_tokens":[0-9]*' | head -1 | cut -d: -f2); i=${i:-0}
  o=$(echo "$line" | grep -o '"output_tokens":[0-9]*' | head -1 | cut -d: -f2); o=${o:-0}
  c=$(echo "$line" | grep -o '"cached_tokens":[0-9]*' | head -1 | cut -d: -f2); c=${c:-0}
  r=$(echo "$line" | grep -o '"reasoning_tokens":[0-9]*' | head -1 | cut -d: -f2); r=${r:-0}
  echo "input=$i output=$o cache=$c reasoning=$r"
}

echo "=== 第一轮请求 (input: system + 1 条 user) ==="
echo "Request 1 input items: 2 (system, user)"
R1=$(curl -sS -N -X POST "$URL" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -H "OpenAI-Beta: responses=v1" \
  -d "$BODY1" 2>/dev/null)
DETAIL1=$(echo "$R1" | get_usage_detail)
TOTAL1=$(echo "$R1" | get_total_context)
echo "  $DETAIL1  => 总上下文: $TOTAL1"
echo ""

echo "=== 第二轮请求 (input: system + user + assistant + user) ==="
echo "Request 2 input items: 4 (system, user, assistant, user)"
R2=$(curl -sS -N -X POST "$URL" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -H "OpenAI-Beta: responses=v1" \
  -d "$BODY2" 2>/dev/null)
DETAIL2=$(echo "$R2" | get_usage_detail)
TOTAL2=$(echo "$R2" | get_total_context)
echo "  $DETAIL2  => 总上下文: $TOTAL2"

if [[ -n "${DEBUG}" ]]; then
  echo ""
  echo "=== 第二轮 response.completed 原始 data 行（含 usage）==="
  echo "$R2" | grep '^data: ' | while read -r d; do
    payload="${d#data: }"
    if [[ "$payload" == *'"response"'* && "$payload" == *'"usage"'* ]]; then
      echo "$payload"
    fi
  done | tail -1
fi
echo ""

echo "=== 对比（总上下文 = input + output + cache + reasoning）==="
echo "Round 1 总上下文: $TOTAL1"
echo "Round 2 总上下文: $TOTAL2"
if [[ -n "$TOTAL1" && -n "$TOTAL2" && "$TOTAL1" -ge 0 && "$TOTAL2" -ge 0 ]]; then
  if [[ "$TOTAL2" -gt "$TOTAL1" ]]; then
    echo "结论: 第二轮总上下文 ($TOTAL2) > 第一轮 ($TOTAL1)，API 随轮次增长正常。"
  elif [[ "$TOTAL2" -eq "$TOTAL1" ]]; then
    echo "结论: 两轮总上下文相同。"
  else
    echo "结论: 第二轮总上下文更小。"
  fi
fi
