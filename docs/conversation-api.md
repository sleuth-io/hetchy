# Conversation API

The supported public API is rooted at `/api/v1`. Browser sessions can use the
same origin WorkOS cookie. External clients use:

```http
Authorization: Bearer hetchy_...
```

API keys are organization-scoped and are created by org admins in
`/settings/org?tab=api-keys`.

## Conversations

Start a conversation and stream the first turn:

```http
POST /api/v1/conversations
Content-Type: application/json
Accept: text/event-stream
```

```json
{
  "id": "optional-client-generated-id",
  "message": "Ship the change",
  "agent": "bob",
  "repository": "owner/repo",
  "model": "sonnet",
  "task_options": {
    "validate": true,
    "review_code_before_push": true,
    "action_pr_checks_for_done": true
  },
  "attachments": [
    {
      "filename": "notes.txt",
      "content_type": "text/plain",
      "data_base64": "..."
    }
  ]
}
```

Add a turn to an existing conversation:

```http
POST /api/v1/conversations/{id}/turns
```

Multipart requests are also accepted. Send a `payload` form field containing
the JSON body above, plus one or more `attachments` file parts.

Read conversations:

```http
GET /api/v1/conversations?limit=20&offset=0&q=search&user=user_id
GET /api/v1/conversations/{id}?include=turns,attachments
```

`include=attachments` returns attachment metadata at both the conversation and
turn levels. Attachment bytes are downloaded from the nested conversation URL
returned as `download_url`.

Stream and control an active turn:

```http
GET  /api/v1/conversations/{id}/events?after_seq=42
POST /api/v1/conversations/{id}/cancel
PATCH /api/v1/conversations/{id}
DELETE /api/v1/conversations/{id}
```

The event stream uses server-sent events with the existing typed block event
names: `block_start`, `block_append`, `block_done`, and `heartbeat`.
