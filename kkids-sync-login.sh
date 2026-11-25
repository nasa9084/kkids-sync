#!/bin/sh
# Signs in to Amazon with the reconciler's own browser, shown through noVNC.
# Open http://<host>:6080/vnc.html, enter the password printed below, sign in,
# and wait for "signed in; session saved". The session goes to $STATE.
set -eu
STATE=${STATE:-/data/storage-state.json}
DISPLAY=:99
export DISPLAY

pass=$(head -c 12 /dev/urandom | od -An -tx1 | tr -d ' \n')
mkdir -p "$HOME/.vnc"
x11vnc -storepasswd "$pass" "$HOME/.vnc/passwd" >/dev/null 2>&1
echo "VNC password: $pass"

Xvfb "$DISPLAY" -screen 0 1366x900x24 -nolisten tcp &
sleep 1
x11vnc -display "$DISPLAY" -rfbauth "$HOME/.vnc/passwd" -rfbport 5900 -localhost -forever -shared -quiet &
websockify --web /usr/share/novnc 6080 localhost:5900 >/dev/null 2>&1 &

exec /usr/local/bin/kkids-sync-reconciler -state "$STATE" login
