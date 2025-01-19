#!/bin/sh
set -ue

go install ./cmd/opp

# warm up, send to /dev/null to avoid output (sterr > stdout)
opp warm/up/opp > /dev/null 2>&1 || true
export OLLAMA_MODELS=$(mktemp -d)
# time opp bmizerany/smol:latest
time opp library/llama3.2:latest
