#!/bin/sh
set -ue

go install ./cmd/opp

# Warm up fresh binary
opp > /dev/null 2>&1 || true

export OLLAMA_MODELS=$(mktemp -d)
echo "OLLAMA_MODELS: $OLLAMA_MODELS"

echo
echo "=== opp pull bmizerany/smol"
time opp pull bmizerany/smol

echo
echo "=== opp push bmizerany/smol"
time opp pull bmizerany/bllama

echo
echo "=== opp push bmizerany/bllama"
time opp push bmizerany/bllama

export OLLAMA_MODELS=$(mktemp -d)
echo "OLLAMA_MODELS: $OLLAMA_MODELS"

echo
echo "=== ollama pull ollama/ollama"
time ollama pull llama3.2

echo
echo "CLEANUP"
rm -rf $OLLAMA_MODELS
