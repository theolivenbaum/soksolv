#!/bin/bash
# Native AOT build of the CLI (no JIT: no startup cost, no tiering warm-up). Output: artifacts/aot/Soksolv.Cli
set -e
export PATH=$PATH:/root/.dotnet DOTNET_CLI_TELEMETRY_OPTOUT=1
cd "$(dirname "$0")/.."
dotnet publish src/Soksolv.Cli -c Release -r linux-x64 -p:PublishAot=true -o artifacts/aot 2>&1 | grep -E "error|Soksolv.Cli ->" || true
ls -la artifacts/aot/Soksolv.Cli
