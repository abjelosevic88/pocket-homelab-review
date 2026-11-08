export interface User { id: number; email: string; name: string; role: 'admin' | 'member'; created_at: string }
export interface APIToken { id: number; name: string; prefix: string; last_used_at: string | null; created_at: string }

export type ReviewStatus = 'draft' | 'in_review' | 'changes_requested' | 'approved' | 'rejected' | 'cancelled'
export type Role = 'owner' | 'approver' | 'reviewer' | 'observer'
export type Verdict = '' | 'approved' | 'changes' | 'rejected'
export type CommentKind = 'comment' | 'question' | 'issue'
export type Severity = '' | 'minor' | 'major' | 'critical'
export type ThreadStatus = 'open' | 'resolved' | 'rejected'

export interface Revision {
  id: number
  review_id: number
  number: number
  kind: string
  filename: string
  mime: string
  size: number
  sha256: string
  source_url: string
  page_count: number
  note: string
  uploaded_by: number
  created_at: string
}

export interface Participant {
  review_id: number
  user_id: number
  name: string
  email: string
  role: Role
  state: 'invited' | 'reviewing' | 'done'
  verdict: Verdict
  verdict_note: string
  verdict_at: string | null
  added_by: number
  added_at: string
  first_opened_at: string | null
  last_seen_at: string | null
  active_seconds: number
}

export interface Presence { conn: string; user_id: number; name: string; color: string; page: number; revision_id: number; active: boolean; since: string }

export interface Review {
  id: number
  title: string
  description: string
  kind: string
  owner_id: number
  status: ReviewStatus
  due_date: string
  require_resolved: boolean
  current_revision_id: number
  started_at: string | null
  decided_at: string | null
  created_at: string
  updated_at: string
  source_app: string
  source_ref: string
  source_url: string
  source_label: string
  participants: Participant[]
  revisions: Revision[]
  my_role: Role
  open_issues: number
  open_threads: number
  threads: number
  open_tasks: number
  page_count: number
  last_activity: string
  live_now?: Presence[]
}

export interface Rect { x: number; y: number; w: number; h: number }
export interface Anchor { rects: Rect[]; quote?: string }

export interface Comment {
  id: number
  review_id: number
  revision_id: number
  parent_id: number
  author_id: number
  author_name: string
  kind: CommentKind
  severity: Severity
  category: string
  body: string
  page: number
  anchor: Anchor | null
  images: number[]
  status: ThreadStatus
  status_by: number
  status_at: string | null
  status_note: string
  deleted: boolean
  created_at: string
  edited_at: string | null
  replies?: Comment[]
}

export interface Task {
  id: number
  review_id: number
  review_title?: string
  comment_id: number
  title: string
  notes: string
  assignee_id: number
  assignee_name: string
  due_date: string
  status: 'open' | 'done'
  created_by: number
  created_at: string
  done_by: number
  done_at: string | null
}

export interface Event {
  id: number
  review_id: number
  review_title?: string
  user_id: number
  user_name: string
  kind: string
  comment_id: number
  task_id: number
  revision_id: number
  page: number
  summary: string
  created_at: string
}

export interface Dashboard {
  waiting: { review: Review; why: 'review' | 'approve' | 'changes' | 'draft' }[]
  to_review: number
  to_approve: number
  open_reviews: number
  open_tasks: number
  tasks: Task[]
  recent: Event[]
  live: { review_id: number; title: string; people: Presence[] }[]
  week_seconds: number
  week_text: string
}

export interface Settings {
  title: string
  date_format: DateFormat
  time_format: TimeFormat
  max_upload_mb: number
  idle_after_seconds: number
  tick_seconds: number
}
export type DateFormat = 'auto' | 'dmy-dot' | 'dmy-slash' | 'mdy-slash' | 'ymd' | 'dmmmy' | 'mmmdy'
export type TimeFormat = 'auto' | '24h' | '12h'

// ---- report ----
export interface PersonRow {
  user_id: number; name: string; email: string; role: Role; state: string; verdict: Verdict; verdict_note: string; verdict_at: string | null
  active_seconds: number; revision_seconds: number; sessions: number; first_opened_at: string | null; last_seen_at: string | null
  pages_read: number; coverage: number; threads: number; issues: number; critical: number; major: number; minor: number; questions: number
  replies: number; closed: number; tasks_assigned: number; tasks_done: number; pages_per_hour: number; seconds_per_page: number
}
export interface ThreadRow {
  id: number; revision: number; page: number; kind: CommentKind; severity: Severity; category: string; status: ThreadStatus; author: string
  body: string; quote: string; created_at: string; status_at: string | null; status_by: string; status_note: string
  replies: { author: string; body: string; created_at: string }[]
}
export interface RevisionInfo { id: number; number: number; filename: string; page_count: number; size: number; created_at: string; threads: number; issues: number }
export interface Report {
  review: { id: number; title: string; description: string; status: ReviewStatus; owner: string; due_date: string; require_resolved: boolean; created_at: string; started_at: string | null; decided_at: string | null }
  revision: RevisionInfo
  revisions: RevisionInfo[]
  generated_at: string
  generated_by: string
  totals: {
    people: number; active_seconds: number; elapsed_seconds: number; sessions: number; threads: number; open_threads: number; comments: number
    questions: number; issues: number; open_issues: number; resolved_issues: number; rejected_issues: number; tasks: number; open_tasks: number
    pages: number; issues_per_page: number; pages_per_hour: number; issues_per_hour: number; avg_coverage: number; fully_covered: number
    unread_pages: number[]; median_page_secs: number; first_activity_at: string; last_activity_at: string
  }
  severity: { severity: Severity; open: number; resolved: number; rejected: number }[]
  people: PersonRow[]
  heatmap: { pages: number; rows: { user_id: number; name: string; role: Role; seconds: number[] }[]; page_totals: number[]; page_issues: number[]; max: number }
  sessions: { user_id: number; name: string; revision_id: number; revision: number; started_at: string; last_at: string; active_seconds: number }[]
  decision: { name: string; role: Role; verdict: Verdict; note: string; at: string | null }[]
  open_issues: ThreadRow[]
  threads: ThreadRow[]
  tasks: Task[]
  timeline: Event[]
}

export interface Notification {
  id: number
  review_id: number
  review_title: string
  kind: 'mention' | 'reply' | 'thread' | 'revision' | 'review' | 'verdict' | 'task'
  actor_id: number
  actor_name: string
  comment_id: number
  title: string
  body: string
  created_at: string
  read_at: string | null
}
