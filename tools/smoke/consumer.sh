#!/usr/bin/env bash
# Consumer smoke test: builds a throwaway host module OUTSIDE this
# repository that requires the library through a replace directive, the way
# a real host program would. It catches leftover internal/ import paths,
# replace directives or anything else that makes the module unusable from
# another module. It runs go build and go vet on the host, then runs it
# against a missing config file and expects a clean "load config" error.
#
# Usage: tools/smoke/consumer.sh   (from anywhere; needs Go on PATH)
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
module=$(awk '/^module /{print $2}' "$repo/go.mod")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cd "$work"
# Keep the host module's resolution independent of any go.work around it.
export GOWORK=off
go mod init example.com/host >/dev/null 2>&1
go mod edit -require="$module@v0.0.0" -replace="$module=$repo"

cat > main.go <<GO
// Command host is a minimal program built on the engine: it registers no
// extensions and runs the service with the configuration path it is given.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"$module/cli"
	"$module/engine"
	"$module/infra/config"
	"$module/infra/logging"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "tools" {
		// The operator command tree, without host commands.
		root := cli.Root()
		root.SetArgs(os.Args[2:])
		if err := root.Execute(); err != nil {
			os.Exit(1)
		}
		return
	}
	configPath := "settings.yml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	profile, err := config.ResolveProfile("")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	logger := logging.New("info", false)
	if err := engine.Run(ctx, configPath, profile, logger, engine.Options{ServiceName: "smoke-host"}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
GO

go mod tidy >/dev/null 2>&1 || { go mod tidy; exit 1; }
echo "go build"; go build -o host .
echo "go vet";   go vet ./...

echo "run with a missing config"
set +e
out=$(./host "$work/does-not-exist.yml" 2>&1)
code=$?
set -e
if [ "$code" -ne 1 ] || ! grep -q "load config" <<<"$out"; then
  echo "FAIL: want exit 1 and a 'load config' error, got exit $code:"
  echo "$out"
  exit 1
fi
echo "ok: $out"

echo "run the tools root"
./host tools --help >/dev/null
echo "consumer smoke test passed"
