#!/bin/sh
set -eu

DIR="/mnt/us/kindleiot"
BIN="$DIR/kindleiot-arm"
CFG="$DIR/config.yaml"
LOG="$DIR/kindleiot.log"
PIDFILE="$DIR/kindleiot.pid"

export DISPLAY="${DISPLAY:-:0}"
export HOME="${HOME:-/mnt/us}"
# Prevent Go 1.27 SIGURG async preemption crash on older ARMv7 Kindle kernel
export GODEBUG="asyncpreemptoff=1"

if [ -f "$PIDFILE" ]; then
  OLD_PID="$(cat "$PIDFILE" 2>/dev/null || true)"
  if [ -n "$OLD_PID" ] && kill -0 "$OLD_PID" 2>/dev/null; then
    kill "$OLD_PID" 2>/dev/null || true
    for _ in 1 2 3 4 5 6; do
      if ! kill -0 "$OLD_PID" 2>/dev/null; then
        break
      fi
      sleep 0.5
    done
  fi
fi

if command -v iptables >/dev/null 2>&1; then
  for p in 8080 51827; do
    if ! iptables -C INPUT -p tcp --dport "$p" -j ACCEPT 2>/dev/null; then
      iptables -I INPUT 1 -p tcp --dport "$p" -j ACCEPT 2>/dev/null || true
    fi
  done
  if ! iptables -C INPUT -p udp --dport 5353 -j ACCEPT 2>/dev/null; then
    iptables -I INPUT 1 -p udp --dport 5353 -j ACCEPT 2>/dev/null || true
  fi
fi

# Disable Kindle Wi-Fi power saving so HomeKit TCP & mDNS events arrive immediately
iwconfig wlan0 power off 2>/dev/null || true
sysctl -w net.ipv4.tcp_keepalive_time=30 net.ipv4.tcp_keepalive_intvl=10 net.ipv4.tcp_keepalive_probes=3 >/dev/null 2>&1 || true

chmod +x "$BIN"
cd "$DIR"
echo "=== Starting KindleIOT $(date) ===" >> "$LOG"
"$BIN" -config "$CFG" -fullscreen >> "$LOG" 2>&1 &
echo $! > "$PIDFILE"
wait $! || true
rm -f "$PIDFILE"
