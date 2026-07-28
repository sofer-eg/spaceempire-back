#!/bin/sh
# Removes the docker containers that this project's tests left behind:
#
#   scripts/reap-test-containers.sh spaceempire.test.run=1785278762225381208
#   scripts/reap-test-containers.sh spaceempire.test=true
#
# Used by the test targets in the Makefile. Containers only reach this script
# when a test binary died without running its cleanup — a `go test -timeout`
# panic, a SIGKILL — because internal/pkg/database/testdb terminates its
# container from TestMain in every ordinary exit.
#
# The filter is restricted to the two labels testdb stamps
# (testdb.LabelKey/LabelValue, testdb.RunLabelKey); anything else is refused.
# This script is what runs `docker rm -f`, so it decides its own scope instead
# of trusting the caller: `reap-test-containers.sh org.testcontainers=true` is
# the plausible thing to reach for when clearing "test containers", and it would
# take out every project on the host. Matching is by label, never by image name.
#
# The second argument is the exit status of the test run. When it is non-zero,
# the sweep repeats until two consecutive passes come up empty, because a
# binary killed while testcontainers was mid-create leaves the daemon to finish
# the job on its own: containers keep materialising for seconds after `go test`
# has already returned, in state "created", and a single sweep walks past them.
# Measured on this box with every package killed at once, an immediate one-shot
# sweep left 11 of them behind.
set -eu

filter=${1:?usage: reap-test-containers.sh <label-filter> [test-exit-status]}
status=${2:-0}
budget=${REAP_BUDGET:-30}

case $filter in
spaceempire.test=* | spaceempire.test.run=*) ;;
*)
	echo "reap: refusing to sweep on '$filter'" >&2
	echo "reap: in scope are only spaceempire.test=<value> and spaceempire.test.run=<id>" >&2
	exit 2
	;;
esac

case $budget in
'' | *[!0-9]*)
	echo "reap: ignoring non-numeric REAP_BUDGET='$budget', using 30" >&2
	budget=30
	;;
esac

failed=0

# sweep exits 0 while there is — or may still be — something to remove, and 1
# once the filter has genuinely come up empty. A docker failure counts as "may
# still be": scoring it as empty is exactly how a sweep ends up reporting
# success having reaped nothing, which is the silent leak all of this exists to
# prevent.
sweep() {
	if ! ids=$(docker ps -aq --filter "label=$filter" 2>&1); then
		echo "reap: docker ps failed: $ids" >&2
		failed=1
		return 0
	fi
	[ -n "$ids" ] || return 1

	echo "removing leaked test containers ($filter):"
	# shellcheck disable=SC2086 # ids is a whitespace-separated list on purpose
	if ! docker rm -f $ids; then
		echo "reap: docker rm failed for: $ids" >&2
		failed=1
	fi
	return 0
}

done_reaping() {
	[ "$failed" -eq 0 ] || echo "reap: cleanup incomplete, containers may remain" >&2
	exit "$failed"
}

if [ "$status" -eq 0 ]; then
	# Every binary exited normally, so nothing can still be in flight.
	sweep || true
	done_reaping
fi

empty=0
elapsed=0
while [ "$empty" -lt 2 ] && [ "$elapsed" -lt "$budget" ]; do
	if sweep; then
		empty=0
	else
		empty=$((empty + 1))
	fi
	sleep 1
	elapsed=$((elapsed + 1))
done

if [ "$empty" -lt 2 ]; then
	echo "reap: gave up after ${budget}s with containers still matching $filter" >&2
	failed=1
fi
done_reaping
