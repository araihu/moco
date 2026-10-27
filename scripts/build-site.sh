#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
export GOWORK=off
mkdir -p tools/bin/site
# Isolate publication tools from the service module and authenticate with go.sumdb.
GOBIN="$PWD/tools/bin/site" go install github.com/araihu/margo/cmd/margo@v0.0.25
GOBIN="$PWD/tools/bin/site" go install github.com/araihu/manja/cmd/manja@v0.1.17
python3 -B scripts/build_site.py
