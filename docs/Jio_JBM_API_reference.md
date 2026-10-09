# Jio / JBM API reference extracted from the supplied Postman collection

Source archive: UBM - Messaging -v3.0.postman_collection.zip
Contained file: UBM - Messaging -v3.0.postman_collection.json
Collection name: UBM - Messaging -v3.0_cs
Source SHA-256: 86208daf9747c06b6b13bbc1cda1d8ce3143d8cb2df132e9e60de0dc54a399f9
Review date: 2026-09-17

This is an inspection of the supplied collection, not a live API test or confirmation of the current production contract. No requests were sent. Credential values, sample recipient numbers, and local upload paths are not reproduced.

## Hosts and authentication

```text
BASE_URL = https://api.businessmessaging.jio.com
TOKEN_GENERATION_URL = https://tgs.businessmessaging.jio.com
```

The Get Token request is GET, uses grant_type=client_credentials and scope=read, and supplies client_id/client_secret in query parameters. Its response script reads access_token. Messaging requests use Authorization: Bearer {{token}}. The collection does not specify token lifetime, rotation, credential scope, or the relationship between portal Service Key and OAuth credentials. Confirm these with Jio. Redact credential-bearing URLs from logs and ask whether Jio supports an alternative credential transport; do not change the method without confirmation.

## Requests present in the collection

### SendMessage/Plain Text

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

Body:
```json
{
  "content": {
    "plainText": "Your ABC Retail order A123 has shipped."
  }
}
```

Saved response example: HTTP 201, a messageId and success=true. This is not proof of handset delivery.

### SendMessage/With TTL

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

Top-level ttl is a duration string; the example uses "1200s". The collection says to provide only one of ttl or expireTime.

### SendMessage/With Expire Time

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

Top-level expireTime is a UTC timestamp. Replace the historical example timestamp before testing. Do not combine it with ttl.

### SendMessage/With Message Traffic Type

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

The collection lists AUTHENTICATION, TRANSACTION, PROMOTION, SERVICEREQUEST, ACKNOWLEDGEMENT. These are wire values, not permission to change an assistant's approved use case.

### SendMessage/Suggested Replies

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - openUrl

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - openUrl WEBVIEW

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - dialerAction

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - Show Location

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - Show Location QUERY

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions - Share Location

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Suggested Actions Create Calendar Event

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Send Media Content Info

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Standalone Card

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Standalone Card without Media

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Standalone card with File ID

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Carousel

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendMessage/Carousel With File ID

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/async?messageId={{uid}}&assistantId={{assistantId}}
```

### SendEvent/Send Typing Event

```http
POST {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantEvents?eventId={{uid}}
```

Body uses eventType=TYPING.

### SendEvent/Send Read Event

```http
POST {{BASE_URL}}/v1/messaging/users/{{phoneNumber}}/assistantEvents?eventId={{uid}}
```

Body uses eventType=MESSAGE_READ and messageId={{userMsgId}}. This acknowledges a user-originated message; it does not retrieve delivery/read status for an outbound message.

### Capabilities/Get Capabilities

```http
GET {{BASE_URL}}/v1/messaging/users/:phNumber/capabilities
```

### Capabilities/Batch Get Capabilities

```http
POST {{BASE_URL}}/v1/messaging/usersBatchGet
```

Body has a phoneNumbers array of E.164 strings. No response example or batch limit is supplied. Do not assume the Google direct-API limits are Jio limits.

### Get Token

```http
GET {{TOKEN_GENERATION_URL}}/v1/oauth/token?grant_type=client_credentials&client_id={{client_id}}&client_secret={{client_secret}}&scope=read
```

### Revoke/Delete Message

```http
DELETE {{BASE_URL}}/v1/messaging/users/:phoneNumber/assistantMessages/{{messageId}}
```

The saved response is labeled as a send-message example and is not reliable evidence of revocation semantics. Obtain current revocation response and event contracts.

### Upload File

```http
POST {{BASE_URL}}/v1/messaging/upload/files
```

The saved request uses binary file mode, not a documented multipart schema. Confirm MIME headers, response file-ID fields, file retention, and size limits.

## Implementation checks

- Each send example generates a new UUID in a pre-request script. Production retries of the same logical message must not blindly generate new IDs; confirm Jio duplicate/idempotency handling.
- Several saved request bodies contain comments, which are not valid strict JSON. Generate clean JSON in production.
- Some non-send endpoints omit assistantId. Confirm whether bearer-token scope selects the assistant, and whether additional parameters are required in the current API.
- Capability, token, event and upload requests do not include complete saved response contracts.
- The collection does not contain incoming webhook payload schemas, callback retry/acknowledgement contracts, or complete error documentation.
- No brand/sub-account creation, assistant creation, verification, launch, tester management, webhook management, billing retrieval, analytics retrieval, or message-status lookup endpoint is present in this collection. This does not establish that Jio offers no such APIs; request a separate management API specification.
- Keep Jio assistant IDs, your own product sender IDs, API credentials, webhook client tokens, and per-message IDs as distinct fields.
- Obtain current production/sandbox hosts, credentials, coverage, throughput, callback IP list, launch permissions and rate card from Jio before enabling production traffic.
