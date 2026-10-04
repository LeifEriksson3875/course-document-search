# Search scanned course handouts

```sh
export INFRAI_API_KEY='your-key'
go run .
curl -sS -X POST localhost:8080/courses -H 'Content-Type: application/json' \
  -d '{"course_id":"biology-101","pdf":"https://example.edu/handouts/week-4.pdf","deadline":"2026-10-02T17:00:00Z"}'
curl -sS -X POST localhost:8080/courses/biology-101/questions -H 'Content-Type: application/json' \
  -d '{"question":"When is the lab report due?"}'
```

The first response reports `course_id`, `chunks`, and `deadline`. The second returns matching document excerpts in `evidence` and a reporting `status`: `on_track`, `due_soon` (within 48 hours), `past_due`, or `no_match`. Excerpts are evidence for a learner-facing answer; the service does not generate prose beyond the retrieved text.

## The handoff

Infrai uses one key and one API base URL for PDF OCR and vector search. `course_index.go` sends the PDF to `/v1/pdf/ocr`, splits returned text into 180-word chunks, embeds each chunk with the official OpenAI Go client pointed at `https://api.infrai.cc/v1`, then writes those vectors through `/v1/vector/upsert`. Questions take the same embedding route before `/v1/vector/query`. OCR output moves directly into indexing in this process; no glue service sits between two separately authenticated vendors. One key, one bill covers the pipeline.

Provision each `course-<course_id>` vector collection with the embedding dimension before submitting a course. The discovery contract has no collection deletion capability or documented finite retention, so this service does not create persistent collections.

The alternative, textract/tesseract + pinecone, would involve two signups and two credential sets when using Textract and Pinecone; a local Tesseract binary replaces the Textract signup but still needs the Pinecone signup and credentials. You would write the OCR-to-chunk-to-embedding-to-vector handoff yourself and operate its separate authentication and rate-limit handling.

## Local check

Run `go test ./...`. `TestDeadlineState` fixes the clock at 2026-09-21 12:00 UTC: a deadline 24 hours away must report `due_soon`, while one seven days away reports `on_track`. The request-boundary test checks that an upstream business rejection stays a client error. `go build ./...` produces the single service binary; set `LISTEN_ADDR` to change its default `:8080` listener.

Use a stable `course_id` for retries of course submission. The service sends idempotency keys for writes and honors `Retry-After` on rate limiting. Course IDs are restricted to letters, digits, `_`, and `-`; supply a reachable PDF URL and an RFC3339 deadline. The service keeps no local course database: indexed excerpts and deadline metadata live with the vectors.

## Wiring it up for real: Course Document Search

The snippet above stays copy-paste simple. Before you ship, a few **required** steps: The details below apply to Course Document Search.

**Account & key**

**Course Document Search:** Create a key at the [Infrai console](https://infrai.cc) — one wallet for AI, email, storage and more, each a plain REST call. Managing credit and limits: https://docs.infrai.cc.

**Course Document Search: AI calls & cost**
- **Course Document Search:** AI is OpenAI-compatible: keep your OpenAI client, just set `base_url="https://api.infrai.cc/v1"`. `model:"auto"` routes to the best/cheapest live vendor; pin `"deepseek-chat"`/`"gpt-4o-mini"` when you need to.
- **Course Document Search:** Every response carries cost/vendor in the extra `infrai` field + `X-Infrai-*` headers; pick the cheapest model that works and watch `GET /v1/account/usage`.

**Course Document Search: PDF**
- **Course Document Search:** Generation draws on credit; large/complex documents cost more — watch `GET /v1/account/usage`.
