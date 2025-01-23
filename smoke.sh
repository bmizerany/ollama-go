#!/bin/sh
set -ue

go install ./cmd/opp

# warm up, send to /dev/null to avoid output (sterr > stdout)
opp warm/up/opp > /dev/null 2>&1 || true
# export OLLAMA_MODELS=$(mktemp -d)
# echo "OLLAMA_MODELS: $OLLAMA_MODELS"
# time opp bmizerany/smol:latest
time opp -trace=/tmp/push.out pull library/llama3.2:latest
time opp -trace=/tmp/pull.out push library/llama3.2:latest
rm -rf $OLLAMA_MODELS
