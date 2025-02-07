#!/bin/bash
set -eu

chmod +x ./opp

if ! which ollama; then
  curl -fsSL https://ollama.com/install.sh | sh
fi

## free up some space first
sudo rm -rf /tmp/*
sudo systemctl stop ollama
sudo rm -rf /usr/share/ollama/.ollama/models

# Clear models
sudo rm -rf /usr/local/lib/ollama/runners
rm -rf ~/.ollama/models

# Warm up the binary
./opp >/dev/null 2>&1 || true

# FIGHT!

echo === OPP

# time ./opp pull llama3.2
# time ./opp pull bmizerany/bllama

echo === OLLAMA
sudo systemctl start ollama
time sudo ollama pull llama3.2
