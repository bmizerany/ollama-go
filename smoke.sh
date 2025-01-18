#!/bin/sh
set -ue

go install ./cmd/opp

# warm up, send to /dev/null to avoid output (sterr > stdout)
opp warm/up/opp 2>/dev/null || true
export OLLAMA_MODELS=$(mktemp -d)
time opp bmizerany/smol:latest
