#!/usr/bin/env bash
# Seeds a workspace for the discovered_from eval scenario.
# Usage: setup.sh <workdir>
#
# Copies the scenario source file into the workdir, runs `quest init`, creates
# test-01 (the assigned task), and accepts it. After this script, the workdir
# is ready for the agent to run against.
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <workdir>" >&2
    exit 2
fi

SEED_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK_DIR="$1"

cd "$WORK_DIR"
cp "$SEED_DIR/foo.go" "$SEED_DIR/desc.md" "$SEED_DIR/ac.md" .

quest init --prefix test >/dev/null
quest create \
    --title "fix date parsing in foo.go" \
    --description @desc.md \
    --acceptance-criteria @ac.md \
    --role coder \
    --tier T2 >/dev/null
quest accept test-01 >/dev/null
