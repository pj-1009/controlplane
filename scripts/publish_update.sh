#!/usr/bin/env bash
# Publish a hand-written update message to the task_updates queue, using
# rabbitmqadmin inside the rabbitmq container (nothing to install locally).
#
# Usage:
#   scripts/publish_update.sh in_progress|done|failed <task_id>
#   scripts/publish_update.sh duplicate <task_id>   # same "done" message twice
#   scripts/publish_update.sh stale <task_id>       # late in_progress with a fresh message_id
#   scripts/publish_update.sh poison                # invalid JSON -> DLQ
#   scripts/publish_update.sh unknown-task          # valid shape, unknown task -> DLQ
#   scripts/publish_update.sh raw '<json>'          # anything you like
set -euo pipefail

cd "$(dirname "$0")/.."

QUEUE="${QUEUE:-task_updates}"
# How to run a command in the RabbitMQ container. make k8s-publish points it
# at the rabbitmq-0 pod with kubectl exec.
read -r -a RABBITMQ_EXEC <<< "${RABBITMQ_EXEC:-docker compose exec -T rabbitmq}"

usage() {
  sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
}

publish() {
  local body="$1"
  echo "publishing to ${QUEUE}: ${body}"
  # The login comes from the rabbitmq container's own environment (set from
  # .env by compose, or from the Secret on Kubernetes), so this script never
  # handles the password. The queue and body are passed as positional
  # arguments to avoid quoting issues.
  "${RABBITMQ_EXEC[@]}" sh -c \
    'rabbitmqadmin -u "$RABBITMQ_DEFAULT_USER" -p "$RABBITMQ_DEFAULT_PASS" \
      publish exchange=amq.default routing_key="$1" \
      properties="{\"content_type\":\"application/json\",\"delivery_mode\":2}" \
      payload="$2"' _ "$QUEUE" "$body"
}

now() {
  # Millisecond UTC timestamp from inside the container, so it works with
  # both GNU and BSD date on the host.
  "${RABBITMQ_EXEC[@]}" date -u +%Y-%m-%dT%H:%M:%S.000Z
}

update() {
  local task_id="$1" status="$2" message_id="${3:-$1:$2}" error="null"
  if [[ "$status" == "failed" ]]; then
    error='"failed by publish_update.sh"'
  fi
  printf '{"message_id":"%s","task_id":"%s","status":"%s","error":%s,"occurred_at":"%s","schema_version":1}' \
    "$message_id" "$task_id" "$status" "$error" "$(now | tr -d '\r')"
}

need_task() {
  [[ -n "${1:-}" ]] || { echo "error: task_id required" >&2; usage; }
}

cmd="${1:-}"
case "$cmd" in
  in_progress|done|failed)
    need_task "${2:-}"
    publish "$(update "$2" "$cmd")"
    ;;
  duplicate)
    need_task "${2:-}"
    body="$(update "$2" done)"
    publish "$body"
    publish "$body"
    ;;
  stale)
    # A new message_id gets past the dedup table, so this exercises the
    # rank check: in_progress after done/failed is ignored as stale.
    need_task "${2:-}"
    publish "$(update "$2" in_progress "$2:in_progress:replay-$RANDOM")"
    ;;
  poison)
    publish '{"this is": not json'
    ;;
  unknown-task)
    publish "$(update "00000000-0000-4000-8000-000000000000" done)"
    ;;
  raw)
    [[ -n "${2:-}" ]] || usage
    publish "$2"
    ;;
  *)
    usage
    ;;
esac
