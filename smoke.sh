#!/bin/sh
set -ue

go install ./cmd/opp

function mktempd {
  mktemp -d $TMPDIR/ollama.XXXXXX
}

# Warm up fresh binary
opp > /dev/null 2>&1 || true

export TMPDIR=/Volumes/data

export OLLAMA_MODELS=$(mktempd)
echo "OLLAMA_MODELS: $OLLAMA_MODELS"

echo
echo "=== opp pull bmizerany/smol"
time opp pull bmizerany/smol

time opp push --from bmizerany/smol bmizerany/cantseeme

echo
echo "## DEBUG"
exit 1

echo
echo "=== opp pull bmizerany/smol"
time opp pull bmizerany/smol

echo
echo "=== opp push bmizerany/smol"
time opp pull bmizerany/bllama

echo
echo "=== opp push bmizerany/bllama"
time opp push bmizerany/bllama

export OLLAMA_MODELS=$(mktempd)
echo "OLLAMA_MODELS: $OLLAMA_MODELS"

echo
echo "=== ollama pull ollama/ollama"
time ollama pull llama3.2
