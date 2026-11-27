# API

JSON over HTTP under `/api/v1`. Authenticate with a session cookie (the web UI) or `Authorization: Bearer <token>` (Settings → API tokens, prefix `prv_`). With Pocket Core, tokens Core gave a paired phone (`pcd_…`) work too and act as that person.

Every review route answers **404** to anyone not on the review, admins included.

## Reviews

| | |
|---|---|
| `GET /reviews?status=open\|closed\|<status>&role=owner\|approver\|reviewer\|observer&q=` | Reviews you are on, with `live_now`. |
| `POST /reviews` | `{title, description?, due_date?, require_resolved?, people?: [{user_id \| email, role}]}` → a draft you own. |
| `GET /reviews/{id}` | Review with participants (incl. `active_seconds`), revisions, counts, `my_role`, `live_now`. |
| `PUT /reviews/{id}` | Owner: `{title, description, due_date, require_resolved}`. |
| `DELETE /reviews/{id}` | Owner: deletes it and its files. |
| `POST /reviews/{id}/status` | Owner: `{action: start \| resume \| cancel \| reopen \| complete, note?}`. |
| `POST /reviews/{id}/verdict` | `{verdict: approved \| changes \| rejected \| "", note?}`. Reviewers finish; approvers (or the owner with no approver) decide. `409` while issues are open and the rule is on. |
| `POST /reviews/{id}/revisions` | Owner, multipart: `file` (PDF), `page_count?`, `note?`. |
| `GET /reviews/{id}/revisions/{vid}/file` | The PDF. `?download=1` sends it as an attachment named after the upload plus the revision (`Contract.pdf` → `Contract_v2.pdf`). |
| `POST /reviews/{id}/participants` | Owner: `{user_id \| email, name?, role}`. |
| `PUT /reviews/{id}/participants/{uid}` | Owner: `{role}`; `owner` hands the review over. |
| `DELETE /reviews/{id}/participants/{uid}` | Owner removes anyone; anyone may remove themselves. |

## Threads and tasks

| | |
|---|---|
| `GET /reviews/{id}/comments` | Threads with `replies`. |
| `POST /reviews/{id}/comments` | Thread: `{revision_id?, page, kind: comment \| question \| issue, severity?, body, images?: [id], anchor?: {rects: [{x,y,w,h}], quote?}}` (0..1 page units). Reply: `{parent_id, body, images?}`. Text or at least one picture. |
| `PUT /reviews/{id}/comments/{cid}` | Author: `{body, images?, kind?, severity?}` (`images` left out keeps them; `[]` removes them). |
| `POST /reviews/{id}/images` | Multipart `file`: a PNG, JPEG, GIF or WebP up to 15 MB for a comment → `{id, mime, size}`. The same picture twice in one review is stored once. Not observers. |
| `GET /reviews/{id}/images/{iid}` | The picture (everyone on the review). |
| `DELETE /reviews/{id}/comments/{cid}` | Author. |
| `POST /reviews/{id}/comments/{cid}/status` | `{status: open \| resolved \| rejected, note}` (note required to reject). Owner, approvers, or the thread's author. |
| `GET/POST /reviews/{id}/tasks`, `PUT/DELETE /reviews/{id}/tasks/{tid}` | `{title, notes?, assignee_id?, due_date?, comment_id?, status: open \| done}`. |
| `GET /tasks?all=1` | Tasks assigned to you. |

## Log and report

| | |
|---|---|
| `GET /reviews/{id}/events?kind=comment&user=&before=&limit=` | Log, newest first. `kind` is a prefix (`issue`, `task`, …) or exact (`comment.resolved`). |
| `GET /reviews/{id}/events.csv`, `/comments.csv` | |
| `GET /reviews/{id}/report?revision=` | Everything on the report page. |
| `GET /reviews/{id}/report.pdf?revision=` | |
| `GET /dashboard` | `to_review`, `to_approve`, `open_tasks`, `week_text`, `waiting`, `live`, `recent`; good for a Homepage widget. |
| `GET /activity` | Log across your reviews. |

## Moving out and in

| | |
|---|---|
| `GET /export.zip` | The reviews you started: `review.json` (each review, its revisions, every comment with its writer, pictures, tasks, the people on it) and the PDFs and pictures beside it. |
| `POST /import` | Multipart `file` (or the zip as the body): adds those reviews with you as their owner. Comments keep their writer when that person has an account here, and carry the writer's name otherwise. A review already brought in (same title and start time) is skipped. |

Pocket Move can copy your reviews out and bring them in: `GET /pocket/portable` describes these two for the link scopes `move:export` and `move:import`. A link outside its scopes gets `403`.

## Search, Day and Hub

Pocket Search, Pocket Day and Pocket Hub read your reviews here on their own once this app is registered with Pocket Core (the manifest says what it offers: search, day and widget, in the category *Work*). Each answers only for the reviews you are on, and changes nothing.

| | |
|---|---|
| `GET /pocket/search?q=&limit=` | Reviews whose title or description has the words, as the Reviews list finds them, open or finished. `snippet` says where it stands ("Waiting for 2 reviewers · updated 3 Nov", "Approved · updated 20 Nov"); `url` is `/reviews/{id}`. A query without a letter or digit finds nothing. |
| `GET /pocket/day/{YYYY-MM-DD}?tz=` | A line per review you created, started, commented on, gave a verdict on or decided that day, and per review of yours someone decided ("1 review approved, 2 commented on"). Opening and reading are left out. Without `tz` the day is the server's. |
| `GET /pocket/widget` | What Home shows under *Waiting for you* (to review or decide first, then your drafts and reviews sent back for changes), then your own reviews waiting for others, with the numbers to review, to decide, waiting for others and open tasks. |

A Core link with `pocket:search`, `pocket:day` or `pocket:widget` opens exactly its one route (and `/auth/me`); `reviews:read` and `reviews:write` do not open them. There is no quick capture: a review needs a document and people.

## Live channel

`GET /reviews/{id}/live?revision=&page=` upgrades to a WebSocket (same origin only). Browser → server: `{"t":"state","page":3,"visible":true,"input":true}` every few seconds and on change; `{"t":"cursor","page":3,"x":0.41,"y":0.2}` (page 0 hides it). Server → browser: `hello`, `presence` (every open tab: user, colour, page, active), `cursor`, `changed` (`what`: comments, tasks, review, people, events: refetch those), `bye` (you were taken off the review).
