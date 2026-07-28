#!/bin/sh
# Removes the docker containers matching a label filter:
#
#   scripts/reap-test-containers.sh spaceempire.test.run=1785278762225381208
#   scripts/reap-test-containers.sh spaceempire.test=true
#
# Used by the test targets in the Makefile. Containers only reach this script
# when a test binary died without running its cleanup — a `go test -timeout`
# panic, a SIGKILL — because internal/pkg/database/testdb terminates its
# container from TestMain in every ordinary exit.
#
# The second argument is the exit status of the test run. When it is non-zero,
# the sweep repeats until two consecutive passes come up empty, because a
# binary killed while testcontainers was mid-create leaves the daemon to finish
# the job on its own: containers keep materialising for seconds after `go test`
# has already returned, in state "created", and a single sweep walks past them.
# Measured on this box with every package killed at once, an immediate one-shot
# sweep left 11 of them behind.
#
# Filtering is always by label, never by image name, so containers belonging to
# anything else on the host are out of scope by construction.
set -eu

filter=${1:?usage: reap-test-containers.sh <label-filter> [test-exit-status]}
status=${2:-0}
budget=${REAP_BUDGET:-30}

sweep() {
	ids=$(docker ps -aq --filter "label=$filter")
	[ -n "$ids" ] || return 1
	echo "removing leaked test containers ($filter):"
	# shellcheck disable=SC2086 # ids is a whitespace-separated list on purpose
	docker rm -f $ids || true
	return 0
}

if [ "$status" -eq 0 ]; then
	# Every binary exited normally, so nothing can still be in flight.
	sweep || true
	exit 0
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
