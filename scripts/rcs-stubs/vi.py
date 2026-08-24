"""A stand-in for Vi RBM, for local end-to-end runs.

Reproduces what makes Vi different from Airtel, which is most of it:

  * OAuth2 client_credentials against a SEPARATE host, Basic-authed, and the
    token endpoint counts requests — this one refuses past 5 to make an
    uncached client fail loudly rather than quietly
  * capability discovery at the GOOGLE-style path, answering 200 with {} for a
    handset that has no RCS rather than an error
  * send at /rcs/v1/phones/{msisdn}/agentMessages/async, taking the caller's
    own messageId
  * no template API at all

Numbers ending in an even digit are reachable.
"""
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs, unquote

FEATURES = ["REVOCATION", "RICHCARD_STANDALONE", "RICHCARD_CAROUSEL",
            "ACTION_DIAL", "ACTION_OPEN_URL"]
TOKEN = "vi-access-token-1"
state = {"tokens_minted": 0, "sends": []}


def reachable(number):
    digits = [c for c in number if c.isdigit()]
    return bool(digits) and int(digits[-1]) % 2 == 0


class Handler(BaseHTTPRequestHandler):
    def _send(self, payload, status=200):
        encoded = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def _bearer_ok(self):
        if self.headers.get("Authorization") != "Bearer " + TOKEN:
            self._send({"error": {"code": 401, "status": "UNAUTHENTICATED"}}, 401)
            return False
        return True

    def do_POST(self):
        parsed = urlparse(self.path)

        if parsed.path == "/auth/oauth/token":
            # clientId:secret base64-encoded, NOT form fields.
            if self.headers.get("Authorization") != "Basic dmktY2xpZW50OnZpLXNlY3JldA==":
                return self._send({"error": "invalid_client"}, 401)
            if parse_qs(parsed.query).get("grant_type") != ["client_credentials"]:
                return self._send({"error": "unsupported_grant_type"}, 400)
            state["tokens_minted"] += 1
            # Vi allows 60 token requests a minute for the whole account. A
            # client that mints one per call would spend that on a single
            # audience check, so this refuses early and loudly.
            if state["tokens_minted"] > 5:
                return self._send({"error": "rate_limited"}, 429)
            return self._send({"access_token": TOKEN, "expires_in": 3600})

        if not self._bearer_ok():
            return

        if parsed.path.endswith("/agentMessages/async"):
            query = parse_qs(parsed.query)
            length = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(length) or b"{}")
            template = (body.get("contentMessage") or {}).get("templateMessage") or {}
            if not template.get("templateCode"):
                return self._send({"error": {"message": "template required"}}, 400)
            msisdn = unquote(parsed.path.split("/phones/")[1].split("/")[0])
            state["sends"].append({
                "msisdn": msisdn,
                "messageId": (query.get("messageId") or [""])[0],
                "templateCode": template["templateCode"],
                "customParams": template.get("customParams"),
                "ttl": body.get("ttl"),
            })
            return self._send({})

        if parsed.path.endswith("/rcsEnabledContacts"):
            length = int(self.headers.get("Content-Length") or 0)
            body = json.loads(self.rfile.read(length) or b"{}")
            users = body.get("users", [])
            if len(users) > 10000:
                return self._send({"Error": "Requested users shouldn't be more than 10000"}, 400)
            return self._send({"rcsEnabledContacts": [u for u in users if reachable(u)]})

        self._send({"error": {"message": "no such endpoint"}}, 404)

    def do_GET(self):
        parsed = urlparse(self.path)

        # Test-only: what the stub has seen.
        if parsed.path == "/_control/state":
            return self._send(state)

        if not self._bearer_ok():
            return

        # Google-style capability discovery (§3.5), the one that speaks the
        # same vocabulary Airtel does.
        if parsed.path.startswith("/rcs/v1/phones/") and parsed.path.endswith("/capabilities"):
            msisdn = unquote(parsed.path.split("/phones/")[1].split("/")[0])
            if not parse_qs(parsed.query).get("botId"):
                return self._send({"error": {"message": "botId required"}}, 400)
            # 200 with an empty object for a handset with no RCS — NOT an error,
            # which is where Vi differs from Airtel.
            if not reachable(msisdn):
                return self._send({})
            return self._send({"features": FEATURES})

        self._send({"error": {"message": "no such endpoint"}}, 404)

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 9098), Handler).serve_forever()
