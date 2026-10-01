#!/usr/bin/env sh
# Builds the Windows release binary into dist/.
# Usage: ./build.sh <version> <api-url>
set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <version> <api-url>" >&2
  exit 1
fi

version="$1"
api_url="$2"

go test ./...
GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -H windowsgui -X main.version=${version} -X main.defaultAPIURL=${api_url}" \
  -o dist/quicktable-print-agent.exe ./cmd/agent

echo "built dist/quicktable-print-agent.exe ${version} -> ${api_url}"
