#!/bin/sh
# Moves the deployment checkout to a commit that is already on origin/main and
# recreates the stack, rolling back to the previous commit if it does not become
# healthy. The CI deploy key may run only this file, from the root-owned
# checkout, via sudo (see README).
set -eu

repo=${LIVE_LAB_DIR:-/opt/live-lab}
sha=${1:-}

case $sha in
'' | *[!0-9a-f]*) echo "usage: update.sh <40-hex commit>" >&2; exit 2 ;;
esac
[ ${#sha} -eq 40 ] || { echo "usage: update.sh <40-hex commit>" >&2; exit 2; }

exec 9>/run/live-lab-update.lock
flock 9

cd "$repo"
git fetch -q origin main
git merge-base --is-ancestor "$sha" origin/main || { echo "$sha is not on origin/main" >&2; exit 3; }
prev=$(git rev-parse HEAD)

# deploy/.env holds secrets plus COMPOSE_PROJECT_NAME and COMPOSE_FILE, so the
# host decides which override files apply.
up() {
	git checkout -q --detach "$1" &&
		(cd deploy && docker compose up -d --build --wait --wait-timeout 120)
}

if up "$sha"; then
	docker image prune -f --filter label=com.docker.compose.project=live-lab >/dev/null
	echo "deployed $sha (was $prev)"
	exit 0
fi

echo "deploy of $sha failed, rolling back to $prev" >&2
up "$prev"
exit 1
