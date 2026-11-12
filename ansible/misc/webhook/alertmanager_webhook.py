#!/usr/bin/env python3
"""Minimal Alertmanager webhook receiver managed by ``pg daemon``.

Alertmanager POSTs one notification per group. For every alert in the
notification this receiver prints a single JSON line to stdout; ``pg daemon``
redirects stdout/stderr to its managed log file, so

    pg daemon logs alertmanager-webhook -n 50

shows only the alert messages (HTTP access logs are suppressed on purpose).
The receiver binds loopback only; Alertmanager runs on the same host with host
networking and points at http://127.0.0.1:18801/alertmanager.

Forwarding to a custom third-party API
--------------------------------------
Alertmanager's generic webhook receiver is the integration point: implement the
API call inside ``deliver()`` below (or call it after the log line). Return
False when the API rejects the alert; the HTTP handler then answers 503 so
Alertmanager retries the notification. Example:

    import urllib.request

    def deliver(status, alert):
        log_alert(status, alert)
        body = json.dumps({"text": alert["annotations"].get("summary", "")}).encode()
        req = urllib.request.Request(
            "https://alert-api.example.com/hook",
            data=body,
            headers={
                "Content-Type": "application/json",
                "Authorization": "Bearer <token>",
            },
        )
        try:
            with urllib.request.urlopen(req, timeout=5) as resp:
                return 200 <= resp.status < 300
        except OSError:
            return False

Credentials must not live in this repository. The daemon inherits the deploy
account's environment (no login shell), so read the token from a 0600 file
owned by that account, or from a wrapper script that exports it.

Alertmanager groups notifications: a retry repeats every alert in the group.
Deduplicate on ``alert["fingerprint"]`` when the third-party API is not
idempotent.
"""

import argparse
import datetime
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def log_alert(status, alert):
    print(
        json.dumps(
            {
                "received_at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds"),
                "status": status,
                "alert": alert,
            },
            ensure_ascii=False,
        ),
        flush=True,
    )


def deliver(status, alert):
    """Handle one alert. Replace/extend this to call a third-party API.

    Returns False to answer 503 and let Alertmanager retry. The default logs
    the alert and always succeeds.
    """
    log_alert(status, alert)
    return True


class AlertmanagerHandler(BaseHTTPRequestHandler):
    server_version = "pgcli-alertmanager-webhook/1.0"

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        if path not in ("/alertmanager", "/"):
            self._reply(404, {"status": "not found"})
            return

        try:
            length = int(self.headers.get("Content-Length", "0"))
            payload = json.loads(self.rfile.read(length) or b"{}")
            if not isinstance(payload, dict):
                raise ValueError("payload must be a JSON object")
        except (ValueError, OSError) as exc:
            self._reply(400, {"status": "bad request", "error": str(exc)})
            return

        status = payload.get("status", "unknown")
        alerts = payload.get("alerts") or []
        for alert in alerts:
            if not deliver(status, alert):
                self._reply(503, {"status": "retry"})
                return

        self._reply(200, {"status": "ok", "alerts": len(alerts)})

    def do_GET(self):
        if self.path.split("?", 1)[0] == "/health":
            self._reply(200, {"status": "ok"})
        else:
            self._reply(404, {"status": "not found"})

    def _reply(self, code, body):
        data = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        # Keep the daemon log limited to alert messages.
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--listen",
        default="127.0.0.1:18801",
        help="host:port to bind (default: 127.0.0.1:18801)",
    )
    args = parser.parse_args()

    host, sep, port = args.listen.rpartition(":")
    if not sep or not host or not port.isdigit():
        parser.error("--listen must be HOST:PORT")
    server = ThreadingHTTPServer((host, int(port)), AlertmanagerHandler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
