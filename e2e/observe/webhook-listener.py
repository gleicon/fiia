#!/usr/bin/env python3
"""Tiny webhook receiver for the Fiia live E2E: prints and appends any Grafana
alert notification POST to /tmp/fiia-webhook.log. Lets you SEE the notification
land when the drift alert fires.

Run with: bash e2e/observe/run-webhook.sh   (Ctrl+C to stop)
"""
import http.server
import json
import sys

LOG = "/tmp/fiia-webhook.log"


class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode(errors="replace")
        try:
            pretty = json.dumps(json.loads(body), indent=2)
        except Exception:
            pretty = body
        with open(LOG, "a") as fh:
            fh.write("=== %s %s ===\n%s\n" % (self.command, self.path, pretty))
        print("=== webhook %s ===\n%s" % (self.path, pretty), flush=True)
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 9999
    print("listening on :%d — alert notifications will print here" % port, flush=True)
    http.server.HTTPServer(("0.0.0.0", port), Handler).serve_forever()