#!/usr/bin/env sh
# Start ownshop in the background (Termux on Android, or any Linux arm64) and open the shop page.
#   ./run-termux.sh          start (log: ownshop.log next to this script)
#   ./run-termux.sh stop     stop it
cd "$(dirname "$0")" || exit 1
DIR=$(pwd)
[ "${1:-}" = stop ] && { pkill -f "$DIR/ownshop" && echo "ownshop stopped"; exit 0; }
termux-wake-lock 2>/dev/null # keep running with the screen off
if pgrep -f "$DIR/ownshop" >/dev/null 2>&1; then
  echo "ownshop is already running"
else
  nohup "$DIR/ownshop" --background >>"$DIR/ownshop.log" 2>&1 &
fi
PORT=2007
for i in $(seq 1 60); do # wait until the shop page answers
  P=$(grep -o 'localhost:[0-9]*' "$HOME/.config/ownshop/ownshop.log" 2>/dev/null | tail -1 | cut -d: -f2)
  PORT=${P:-2007}
  { curl -fs "http://127.0.0.1:$PORT/health" || wget -qO- "http://127.0.0.1:$PORT/health"; } >/dev/null 2>&1 && break
  sleep 1
done
termux-open-url "http://127.0.0.1:$PORT" 2>/dev/null
echo "ownshop: open http://127.0.0.1:$PORT in the phone's browser"
