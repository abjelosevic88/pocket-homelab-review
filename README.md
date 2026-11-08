<p align="center">
  <img src="web/public/favicon.svg" width="72" alt="">
</p>
<h1 align="center">Pocket Review</h1>
<p align="center">
  Review a document together: comments pinned to the page, issues that get resolved or rejected, tasks, a strict sign-off, live presence, and a report of who spent how long on what.<br>
  <b>One binary · one SQLite file plus the documents · one volume.</b>
</p>
<p align="center">
  <a href="https://github.com/abjelosevic88/pocket-homelab-review/actions"><img src="https://github.com/abjelosevic88/pocket-homelab-review/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/abjelosevic88/pocket-homelab-review/pkgs/container/pocket-homelab-review"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%7C%20arm64%20%7C%20armv7-blue" alt="image"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-green" alt="MIT"></a>
</p>

---

Peer review tools like SmartBear Collaborator are built for enterprises and priced that way. This is the same core idea, small enough for a home server or a small team. It starts with PDFs, and the model is ready for other sources.

- **One review per document.** The owner uploads a PDF and puts people on it as **approver** (the strict sign-off), **reviewer** or **observer**. Anyone not on the review gets a 404, admins included.
- **Comments that point at something.** *Select text* and press *Comment*, or switch to *Select area* and draw a box on the page. A thread is a **comment**, a **question** or an **issue** (minor / major / critical) with replies. Threads are **resolved** or **rejected** (rejecting needs a reason), and can be reopened.
- **Whose threads you see.** Filter by who started them — only yours, one or more colleagues, everyone but you, any mix. The pins on the page follow the filter, and it is remembered per review.
- **Tasks.** Turn any thread into a task with an assignee and a due date, or add one directly. *Tasks* lists everything assigned to you across reviews.
- **Revisions.** Upload v2, v3…: open threads carry over, everyone's verdict is cleared so they look again, and older revisions stay viewable with their own threads.
- **Compare revisions.** Any two revisions side by side: words taken out are red on the left, words put in are green on the right, with a count and *next / previous change*. One zoom for both sides, and scrolling one side keeps the other on the same text (or turn sync off). On a phone the two sides take turns.
- **Zoom like a viewer.** Ctrl/⌘ + scroll wheel or a trackpad pinch zooms around the pointer; pages redraw sharp once you stop.
- **Mention people.** Type `@` in a thread, a reply or an edit and pick someone on the review from the list (or `@everyone`). `@sani`, a full name or the part of an email before the @ all work. Mentions are highlighted, yours stand out, and the person is notified.
- **Edit what you wrote.** The ⋯ next to your own comment or reply edits or deletes it, even with the thread folded; an edited message says so.
- **Notifications.** The bell in the top bar collects mentions, replies in threads you wrote in, your threads resolved or rejected, new versions, being added to a review, the review starting or being decided, verdicts on your reviews and tasks given to you. Switch on *Notifications here* (Settings → Notifications) to get them as pop-ups in the browser or on the phone (on an iPhone: add Review to the Home Screen first), and choose which kinds you want.
- **Pictures in threads.** Paste a screenshot (⌘V / Ctrl+V), drop one or pick it with the picture button — in a new thread, a reply or an edit. Click one to see it large.
- **A real decision.** Reviewers finish with *looks good / needs changes / reject*. Approvers decide: **approve** (blocked while any issue is still open, unless you turn that rule off), **request changes** or **reject**. With several approvers, every one of them has to approve.
- **Everything is logged.** Created, invited, opened, uploaded, commented, replied, resolved, rejected, reopened, task added or done, verdicts, decisions, reading sessions. The log is append-only, filterable, and downloadable as CSV.
- **Review time that means something.** The server keeps the clock, not the browser. Time counts only while the document is open in a visible tab *and* the person did something (scroll, mouse, keys, zoom) recently. Several tabs count once, and the time is credited to the page in view. That gives per-person totals, reading sessions, page coverage, and a page-by-page attention map.
- **Live.** See who has the document open, on which page, active or idle; click someone to jump to their page; watch their cursor if you want. New comments, replies, state changes and tasks appear on everyone's screen as they happen.
- **The report.** Sign-off; per-person time, sessions, coverage, issues found, replies, closed threads and pace; issues by severity and state; *where the time went* (people × pages heatmap, with the pages nobody read called out); sessions on a timeline; open issues; tasks; every thread; the full log. Inspection rate (pages/hour) and defect density (issues/page) come from the same numbers. The PDF version is the record you file.

<p align="center"><img src="docs/screenshots/review.png" width="900" alt="A contract under review: pinned issues and comments on the page, threads on the right, and another reviewer's live cursor"></p>

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/report.png" alt="The report: totals, sign-off, issues by severity, people with coverage, the attention heatmap"></td>
    <td width="50%"><img src="docs/screenshots/thread.png" alt="An issue raised from a text selection and an area comment, with resolve / reject / make task"></td>
  </tr>
  <tr>
    <td align="center"><sub>The report: who looked at what, for how long, and what they found.</sub></td>
    <td align="center"><sub>Two people on the same page: an issue from a text selection, an area comment.</sub></td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/reviews.png" alt="Reviews you are on, with status, your role, open issues and who is in them right now"></td>
    <td width="50%"><img src="docs/screenshots/home.png" alt="Home: what is waiting for you, your tasks, recent activity"></td>
  </tr>
  <tr>
    <td align="center"><sub>Every review you are on.</sub></td>
    <td align="center"><sub>Home: what is waiting for you.</sub></td>
  </tr>
</table>

## Quick start

```sh
docker run -d --name pocket-review -p 8080:8080 -v ./data:/data ghcr.io/abjelosevic88/pocket-homelab-review:latest
```

Open http://localhost:8080 and create the first account (it becomes the admin). Behind HTTPS set `BASE_URL` and `SECURE_COOKIES=true`. The live channel is a WebSocket on `/api/v1/reviews/{id}/live`; Caddy, Traefik and nginx with the usual upgrade headers pass it through (see [docs/reverse-proxy.md](docs/reverse-proxy.md)).

Try it with sample data and no login:

```sh
make web build && DATA_DIR=./data DEMO_DATA=true AUTH_MODE=none ./pocket-review
```

## How a review goes

1. **Owner:** *New review* → drop the PDF, add an approver and reviewers (pick an account or type an email; unknown emails get an account that signs in with single sign-on, or an admin sets a password). The review starts as a **draft**.
2. **Owner:** *Start review*. Everyone on it sees it under *Home → Waiting for you*.
3. **Reviewers:** read, select text or draw boxes, raise issues, ask questions, reply. *Finish review* with a verdict.
4. **Owner / approvers:** resolve or reject threads, turn them into tasks. Approvers *Decide*:
   - **Request changes** → the owner uploads v2 → the review is back in review, verdicts are cleared, open threads carry over.
   - **Approve** → refused while issues are open (if the rule is on); otherwise approved once every approver approves.
   - **Reject** → ends the review.
5. Anyone on it opens **Report** → *Download PDF*.

Roles in detail:

| | Owner | Approver | Reviewer | Observer |
|---|:-:|:-:|:-:|:-:|
| Read, see the log and the report | ✓ | ✓ | ✓ | ✓ |
| Comment, raise issues, reply, add tasks | ✓ | ✓ | ✓ | |
| Resolve / reject **any** thread | ✓ | ✓ | own threads | |
| Verdict | decides when there is no approver | **decides** | finishes | |
| Upload revisions, manage people, start / cancel / reopen, delete | ✓ | | | |

## Pocket Core

With `CORE_URL` set the app registers with [Pocket Core](https://github.com/abjelosevic88/pocket-homelab-core), offers **Sign in with Pocket Core**, accepts the tokens Core hands to paired phones, and publishes `review.review_started`, `review.comment_added` and `review.review_decided` (each with `audience_emails`, the people on the review). Without it everything works the same.

## Documentation

- [Configuration](docs/configuration.md): every environment variable
- [How review time is measured](docs/review-time.md): the clock, sessions, coverage, the report's numbers
- [API](docs/api.md): JSON API, tokens, the live channel
- [Reverse proxy](docs/reverse-proxy.md) · [Backup](docs/backup.md) · [Roadmap](docs/roadmap.md)

## Build

Go 1.26 and Node 22.

```sh
make web build     # UI into web/dist, then a static binary that embeds it
make test          # go vet + go test + tsc
make docker
```

PDF rendering is Mozilla [pdf.js](https://mozilla.github.io/pdf.js/) (legacy build, so older iPhones work too); report PDFs are drawn with [go-pdf/fpdf](https://github.com/go-pdf/fpdf) and DejaVu Sans.

## License

MIT
