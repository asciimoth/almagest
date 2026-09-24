#!/usr/bin/env bash
set -euo pipefail

source /e2e/lib/common.sh

interval_log=/tmp/almagest-e2e-time-trigger-interval.log
ready_log=/tmp/almagest-e2e-time-trigger-ready.log

line_count() {
	local path="$1"
	if [[ ! -f "$path" ]]; then
		echo 0
		return
	fi
	wc -l <"$path"
}

count_at_least() {
	local path="$1"
	local expected="$2"
	(( $(line_count "$path") >= expected ))
}

wait_count_at_least() {
	local path="$1"
	local expected="$2"
	local description="$3"
	retry "$ALMAGEST_E2E_TIMEOUT" "$description" count_at_least "$path" "$expected" \
		|| fail "$description did not occur"
}

wait_count_stable() {
	local path="$1"
	local quiet_ticks="${2:-8}"
	local deadline=$((SECONDS + ALMAGEST_E2E_TIMEOUT))
	local previous
	local current
	local stable=0

	previous="$(line_count "$path")"
	while ((SECONDS <= deadline)); do
		sleep 0.05
		current="$(line_count "$path")"
		if [[ "$current" == "$previous" ]]; then
			((stable += 1))
			if ((stable >= quiet_ticks)); then
				echo "$current"
				return 0
			fi
		else
			previous="$current"
			stable=0
		fi
	done
	return 1
}

apply_timer() {
	local mode="$1"
	local interval="$2"
	local emit_on_ready="$3"
	jq -n \
		--arg mode "$mode" \
		--arg interval "$interval" \
		--argjson emitOnReady "$emit_on_ready" \
		'{"config":{"mode":$mode,"interval":$interval,"dailyTime":"00:00","emitOnReady":$emitOnReady}}' \
		| cli graph apply-menu --name "Interval Timer" >/dev/null
}

apply_daily_timer() {
	local daily_time="$1"
	jq -n \
		--arg dailyTime "$daily_time" \
		'{"config":{"mode":"daily","interval":"80ms","dailyTime":$dailyTime,"emitOnReady":false}}' \
		| cli graph apply-menu --name "Interval Timer" >/dev/null
}

timer_worker_count() {
	local node="$1"
	cli engine workers-snapshot | jq -er --argjson node "$node" \
		'([.workers[] | select(.node == $node and .worker_name == "time trigger")] | length)'
}

timer_worker_count_is() {
	local node="$1"
	local expected="$2"
	[[ "$(timer_worker_count "$node")" == "$expected" ]]
}

wait_timer_worker_count() {
	local node="$1"
	local expected="$2"
	retry "$ALMAGEST_E2E_TIMEOUT" "timer worker count $expected" \
		timer_worker_count_is "$node" "$expected" \
		|| fail "timer worker count did not become $expected"
}

start_daemon

for node in "Interval Timer" "Interval Recorder" "Ready Timer" "Ready Recorder"; do
	wait_graph_node "$node" || fail "$node node missing"
done

timer="$(node_id_by_name "Interval Timer")"
recorder="$(node_id_by_name "Interval Recorder")"

wait_node_label "$timer" "every 80ms" \
	|| fail "interval timer did not restore its configured schedule"
wait_timer_worker_count "$timer" 1
wait_count_at_least "$interval_log" 2 "initial interval triggers"

# A rejected form does not replace the last valid schedule.
before_invalid="$(line_count "$interval_log")"
apply_timer interval 0s false
wait_node_label "$timer" "every 80ms" \
	|| fail "invalid timer edit replaced the last valid schedule"
wait_timer_worker_count "$timer" 1
wait_count_at_least "$interval_log" "$((before_invalid + 2))" \
	"interval triggers after invalid edit"

# Disabled mode must cancel the tracked worker and stop new events.
apply_timer "" 80ms false
wait_node_label "$timer" "time trigger" \
	|| fail "timer did not enter disabled mode"
wait_timer_worker_count "$timer" 0
disabled_count="$(wait_count_stable "$interval_log")" \
	|| fail "interval commands did not settle after disabling the timer"
[[ "$(wait_count_stable "$interval_log")" == "$disabled_count" ]] \
	|| fail "disabled timer continued to emit triggers"

# Rapidly replacing the schedule must converge to one current worker.
for interval in 35ms 45ms 30ms 40ms 25ms 50ms; do
	apply_timer interval "$interval" false
done
wait_node_label "$timer" "every 50ms" \
	|| fail "timer did not converge on the latest interval"
wait_timer_worker_count "$timer" 1
before_reenabled="$(line_count "$interval_log")"
wait_count_at_least "$interval_log" "$((before_reenabled + 2))" \
	"triggers after timer re-enable"

# Daily mode uses a separate parser and next-run calculation. Schedule far
# enough ahead that the worker is active before the selected second arrives.
daily_time="$(date --date='+10 seconds' +%H:%M:%S)"
before_daily="$(line_count "$interval_log")"
apply_daily_timer "$daily_time"
wait_node_label "$timer" "daily $daily_time" \
	|| fail "timer did not enter daily mode"
wait_timer_worker_count "$timer" 1
wait_count_at_least "$interval_log" "$((before_daily + 1))" \
	"daily trigger"
apply_timer interval 50ms false
wait_node_label "$timer" "every 50ms" \
	|| fail "timer did not return from daily mode"

# The active timer remains a worker while detached, but cannot reach the
# command node until the output link returns.
trigger_link="$(link_id_by_port_names "Interval Timer" Trigger "Interval Recorder" Trigger)"
cli graph delete-link --id "$trigger_link" >/dev/null
wait_timer_worker_count "$timer" 1
detached_count="$(wait_count_stable "$interval_log")" \
	|| fail "interval commands did not settle after trigger detach"
[[ "$(wait_count_stable "$interval_log")" == "$detached_count" ]] \
	|| fail "detached timer still reached the command node"
add_link_by_port_name "$recorder" left Trigger "$timer" right Trigger >/dev/null
wait_count_at_least "$interval_log" "$((detached_count + 2))" \
	"triggers after output relink"

# A disabled emit-on-ready timer must produce one event for each daemon build.
wait_count_at_least "$ready_log" 1 "initial emit-on-ready trigger"
ready_before_restart="$(wait_count_stable "$ready_log")" \
	|| fail "ready commands did not settle before restart"
[[ "$ready_before_restart" == 1 ]] \
	|| fail "emit-on-ready produced $ready_before_restart initial triggers, want 1"

interval_before_restart="$(line_count "$interval_log")"
stop_daemon
start_daemon

timer="$(node_id_by_name "Interval Timer")"
wait_node_label "$timer" "every 50ms" \
	|| fail "restart did not preserve the latest interval"
wait_timer_worker_count "$timer" 1
wait_count_at_least "$interval_log" "$((interval_before_restart + 2))" \
	"interval triggers after restart"
wait_count_at_least "$ready_log" 2 "emit-on-ready trigger after restart"
ready_after_restart="$(wait_count_stable "$ready_log")" \
	|| fail "ready commands did not settle after restart"
[[ "$ready_after_restart" == 2 ]] \
	|| fail "restart produced $ready_after_restart total ready triggers, want 2"

log "case passed"
