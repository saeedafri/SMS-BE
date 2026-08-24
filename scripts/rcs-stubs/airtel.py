"""A stand-in for Airtel IQ, for local end-to-end runs.

Transcribed from the vendor doc, reproducing the behaviours that actually cost
work to get right:

  * capability discovery is a GET carrying a JSON body
  * every response is an envelope whose `success` flag is authoritative
  * an UNREACHABLE handset comes back as a FAILED envelope wrapping a Google 404
  * template registration returns PENDING, never approved on the spot
  * send returns a messageRequestId, which is the only key later webhooks quote

Numbers ending in an even digit are reachable.
"""
import json
import uuid
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

FEATURES = ["RICHCARD_STANDALONE", "RICHCARD_CAROUSEL", "ACTION_DIAL",
            "ACTION_OPEN_URL", "PDF_IN_RICH_CARDS"]

TEMPLATES = {}


def reachable(number):
    digits = [c for c in number if c.isdigit()]
    return bool(digits) and int(digits[-1]) % 2 == 0


class Handler(BaseHTTPRequestHandler):
    def _body(self):
        length = int(self.headers.get("Content-Length") or 0)
        return json.loads(self.rfile.read(length) or b"{}")

    def _send(self, payload, status=200):
        encoded = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def _authorised(self):
        if self.headers.get("Authorization") != "Basic ZmFrZTpmYWtl":
            self._send({"success": False, "code": 401, "message": "Unauthorized"}, 401)
            return False
        return True

    def _account_present(self, body):
        for field in ("customerId", "subAccountId", "agentId"):
            if not body.get(field):
                self._send({
                    "success": False, "code": 400,
                    "message": "Validation Error - Mandatory Request Parameter(s) cannot be null!",
                }, 400)
                return False
        return True

    def do_GET(self):
        if not self._authorised():
            return
        path = urlparse(self.path).path


        # Test-only: list what has been registered, so a browser test can find
        # the id the carrier issued without a bearer token of its own.
        if urlparse(self.path).path == "/_control/templates":
            return self._send(list(TEMPLATES.values()))

        # Fetch a template: identifiers travel as query parameters here.
        if path.endswith("/v1/rcs/template"):
            query = parse_qs(urlparse(self.path).query)
            template_id = (query.get("templateId") or [""])[0]
            template = TEMPLATES.get(template_id)
            if not template:
                return self._send({"success": False, "code": 400,
                                   "message": "Template not found for provided templateId"}, 400)
            return self._send({"success": True, "code": 200, "message": "success",
                               "rcsTemplate": template})

        body = self._body()
        if not body.get("agentId"):
            return self._send({"success": False, "code": 400,
                               "message": "AgentId should not be empty!"}, 400)

        if path.endswith("/rcs/capabilities"):
            number = body.get("phoneNumber", "")
            if not reachable(number):
                return self._send({
                    "success": False, "code": 400,
                    "message": ("Failed to fetch capabilities: 404 Not Found\\nGET "
                                "https://asia-rcsbusinessmessaging.googleapis.com/v1/phones/"
                                f"{number}/capabilities \\\"status\\\" : \\\"NOT_FOUND\\\""),
                }, 400)
            return self._send({"success": True, "code": 200, "message": "success",
                               "features": FEATURES})

        if path.endswith("/users/reachability"):
            users = body.get("users", [])
            if not 500 <= len(users) <= 10000:
                return self._send({
                    "success": False, "code": 400,
                    "message": "Users list must contain between 500 and 10000 unique numbers!",
                }, 400)
            return self._send({"success": True, "code": 200, "message": "success",
                               "reachableUsers": [u for u in users if reachable(u)]})

        self._send({"success": False, "code": 404, "message": "no such endpoint"}, 404)

    def do_POST(self):
        if not self._authorised():
            return
        path = urlparse(self.path).path
        body = self._body()
        if not self._account_present(body):
            return

        if path.endswith("/v1/rcs/template"):
            if not body.get("templateName"):
                return self._send({"success": False, "code": 400,
                                   "message": "Validation Error - templateName cannot be null"}, 400)
            template_id = uuid.uuid4().hex
            # PENDING, never approved on the spot: their review takes up to 24
            # hours and arrives on the webhook.
            TEMPLATES[template_id] = {
                "templateId": template_id,
                "templateName": body["templateName"],
                "templateCategory": body.get("templateCategory"),
                "templateData": body.get("templateData"),
                "templateStatus": "PENDING",
            }
            return self._send({"success": True, "code": 200, "message": "success",
                               "rcsTemplate": TEMPLATES[template_id]})

        if path.endswith("/v1/rcs/message/send"):
            template_id = body.get("templateId")
            if not template_id:
                return self._send({
                    "success": False, "code": 400,
                    "message": "Either message content or templateId is required for sending message!!",
                }, 400)
            if template_id not in TEMPLATES:
                return self._send({"success": False, "code": 400,
                                   "message": "Template not found for provided templateId"}, 400)
            if TEMPLATES[template_id]["templateStatus"] != "APPROVED":
                return self._send({"success": False, "code": 400,
                                   "message": "Template is not active/approved!!"}, 400)
            return self._send({"success": True, "code": 200, "status": "INITIATED",
                               "messageRequestId": uuid.uuid4().hex})

        # Test-only: mark a template approved, the way their reviewer would.
        # Not part of Airtel's API — it exists so a browser test can move a
        # template through a 24-hour review in one call.
        if path == "/_control/approve-template":
            template_id = body.get("templateId")
            if template_id in TEMPLATES:
                TEMPLATES[template_id]["templateStatus"] = "APPROVED"
                return self._send({"ok": True})
            return self._send({"ok": False, "known": list(TEMPLATES)}, 404)

        self._send({"success": False, "code": 404, "message": "no such endpoint"}, 404)

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", 9099), Handler).serve_forever()
