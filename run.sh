#!/bin/bash
set -eu

export CLOUDSDK_COMPUTE_REGION="us-central1"
export CLOUDSDK_COMPUTE_ZONE="us-central1-a"
export CLOUDSDK_CORE_PROJECT="ollama"

INSTANCE="blake"
USER="ollama"
REMOTE="$USER@$INSTANCE"

echo "# deploying binaries to $INSTANCE"
GOOS=linux GOARCH=amd64 go build -o /tmp/opp ./cmd/opp
gcloud compute scp /tmp/opp $REMOTE:~/opp

GOOS=linux GOARCH=amd64 go test -c -o /tmp/oppbench ./cmd/oppbench
gcloud compute scp /tmp/oppbench $REMOTE:~/oppbench

REMOTESCRIPT=$(sed -n '/^# --- REMOTESCRIPT ---/,$p' $0)

logdir=$HOME/.share/ollama-go/runs/$(date +%s)
echo "# writing logs to $logdir"
mkdir -p $logdir

echo "# running REMOTESCRIPT on $INSTANCE"
echo "$REMOTESCRIPT" | gcloud compute ssh $REMOTE --command "bash /dev/stdin $@" 2>&1 | tee $logdir/run.log | rtss

echo "# copying logs/traces"
gcloud compute scp $REMOTE:~/trace.out $logdir/trace.out

# kitty hyperlink for "go tool trace $logdir/trace.out" using OSC 8
echo "log:   file://$logdir/run.log"
echo "trace: file://$logdir/trace.out"

exit 0 # exit client-side script

# --- REMOTESCRIPT -----------------------------------------------------
#!/bin/bash
set -eu

echo
echo
echo "=== REMOTE $(hostname) ==="

MODEL=${1:-"llama3.3"}

set -x
sudo systemctl stop ollama
killall -9 opp      2>&1 || true
killall -9 oppbench 2>&1 || true

(ps aux | grep -q opp) && echo "opp(bench) still running... terminating"

echo "=== OPP ====="
rm -rf ~/.ollama/models
time ./opp pull $MODEL

# real	4m40.868s
sleep 2
echo "=== OLLAMA ==="
rm -rf ~/.ollama/models
sudo systemctl start ollama
time ollama pull $MODEL
