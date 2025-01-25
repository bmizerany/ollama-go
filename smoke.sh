#!/bin/sh
set -ue

go install ./cmd/opp

# warm up, send to /dev/null to avoid output (sterr > stdout)
opp warm/up/opp > /dev/null 2>&1 || true

# export OLLAMA_MODELS=$(mktemp -d)
# echo "OLLAMA_MODELS: $OLLAMA_MODELS"
# time opp bmizerany/smol:latest

echo
echo "=== PULL"
time opp -trace=/tmp/pull.out pull bmizerany/smol:latest

echo
echo "=== PUSH"
# export GODEBUG=http2debug=1
time opp -trace=/tmp/push.out push --to ollama.com/bmizerany/test:latest ollama.com/library/llama3.2:latest

# echo "CLEANUP"
# rm -rf $OLLAMA_MODELS
