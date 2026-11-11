package store

// User is an account that can sign in. The first user is the admin; admins
// manage users and box-wide settings but see only the reviews they are on,
// like everyone.
type User struct {
	ID           int64  `json:"id"`
	Email        string `json:"email"`
	Name         string `json:"name"`
	PasswordHash string `json:"-"`
	Role         string `json:"role"` // admin | member
	CreatedAt    string `json:"created_at"`
}

// APIToken is a personal access token (hash stored, plaintext shown once).
type APIToken struct {
	ID         int64   `json:"id"`
	UserID     int64   `json:"user_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
}

// Review statuses.
const (
	StatusDraft            = "draft"
	StatusInReview         = "in_review"
	StatusChangesRequested = "changes_requested"
	StatusApproved         = "approved"
	StatusRejected         = "rejected"
	StatusCancelled        = "cancelled"
)

// Participant roles.
const (
	RoleOwner    = "owner"
	RoleApprover = "approver"
	RoleReviewer = "reviewer"
	RoleObserver = "observer"
)

// Review is one document going through review.
type Review struct {
	ID                int64   `json:"id"`
	Title             string  `json:"title"`
	Description       string  `json:"description"`
	Kind              string  `json:"kind"`
	OwnerID           int64   `json:"owner_id"`
	Status            string  `json:"status"`
	DueDate           string  `json:"due_date"`
	RequireResolved   bool    `json:"require_resolved"`
	CurrentRevisionID int64   `json:"current_revision_id"`
	StartedAt         *string `json:"started_at"`
	DecidedAt         *string `json:"decided_at"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	// Source: the app the document came from (office, documents), a stable
	// reference that app looks up, a link back to the original and a label.
	SourceApp   string `json:"source_app"`
	SourceRef   string `json:"source_ref"`
	SourceURL   string `json:"source_url"`
	SourceLabel string `json:"source_label"`

	// Joined / derived, read-only.
	Participants []Participant `json:"participants"`
	Revisions    []Revision    `json:"revisions"`
	MyRole       string        `json:"my_role"`
	OpenIssues   int           `json:"open_issues"`
	OpenThreads  int           `json:"open_threads"`
	Threads      int           `json:"threads"`
	OpenTasks    int           `json:"open_tasks"`
	PageCount    int           `json:"page_count"`
	LastActivity string        `json:"last_activity"`
}

// Locked reports whether the review is finished, so comments and tasks are read-only.
func (r *Review) Locked() bool {
	return r.Status == StatusApproved || r.Status == StatusRejected || r.Status == StatusCancelled
}

// TakesComments reports whether people may still talk on the review. A
// decided review (approved or rejected) stays open for comments and replies,
// so a reviewer can still have their say after the approver's call; only a
// cancelled review is read-only.
func (r *Review) TakesComments() bool { return r.Status != StatusCancelled }

// Revision is one version of the document.
type Revision struct {
	ID          int64  `json:"id"`
	ReviewID    int64  `json:"review_id"`
	Number      int    `json:"number"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	Mime        string `json:"mime"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	StoragePath string `json:"-"`
	SourceURL   string `json:"source_url"`
	PageCount   int    `json:"page_count"`
	Note        string `json:"note"`
	UploadedBy  int64  `json:"uploaded_by"`
	CreatedAt   string `json:"created_at"`
}

// Participant is a person on a review.
type Participant struct {
	ReviewID      int64   `json:"review_id"`
	UserID        int64   `json:"user_id"`
	Name          string  `json:"name"`
	Email         string  `json:"email"`
	Role          string  `json:"role"`
	State         string  `json:"state"`
	Verdict       string  `json:"verdict"`
	VerdictNote   string  `json:"verdict_note"`
	VerdictAt     *string `json:"verdict_at"`
	AddedBy       int64   `json:"added_by"`
	AddedAt       string  `json:"added_at"`
	FirstOpenedAt *string `json:"first_opened_at"`
	LastSeenAt    *string `json:"last_seen_at"`
	ActiveSeconds int     `json:"active_seconds"`
}

// Rect is a box on a page in 0..1 units of the page's width and height.
type Rect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Anchor is where on the page a thread points: boxes plus the text they cover.
type Anchor struct {
	Rects []Rect `json:"rects"`
	Quote string `json:"quote,omitempty"`
}

// Comment is a thread root or a reply.
type Comment struct {
	ID         int64   `json:"id"`
	ReviewID   int64   `json:"review_id"`
	RevisionID int64   `json:"revision_id"`
	ParentID   int64   `json:"parent_id"`
	AuthorID   int64   `json:"author_id"`
	AuthorName string  `json:"author_name"`
	Kind       string  `json:"kind"`
	Severity   string  `json:"severity"`
	Category   string  `json:"category"`
	Body       string  `json:"body"`
	Page       int     `json:"page"`
	Anchor     *Anchor `json:"anchor"`
	Images     []int64 `json:"images"` // pictures pasted into it (ids of the review's images)
	Status     string  `json:"status"`
	StatusBy   int64   `json:"status_by"`
	StatusAt   *string `json:"status_at"`
	StatusNote string  `json:"status_note"`
	Deleted    bool    `json:"deleted"`
	CreatedAt  string  `json:"created_at"`
	EditedAt   *string `json:"edited_at"`

	Replies []Comment `json:"replies,omitempty"`
}

// Task is a to-do from a review.
type Task struct {
	ID           int64   `json:"id"`
	ReviewID     int64   `json:"review_id"`
	ReviewTitle  string  `json:"review_title,omitempty"`
	CommentID    int64   `json:"comment_id"`
	Title        string  `json:"title"`
	Notes        string  `json:"notes"`
	AssigneeID   int64   `json:"assignee_id"`
	AssigneeName string  `json:"assignee_name"`
	DueDate      string  `json:"due_date"`
	Status       string  `json:"status"`
	CreatedBy    int64   `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
	DoneBy       int64   `json:"done_by"`
	DoneAt       *string `json:"done_at"`
}

// Event is one line of a review's log.
type Event struct {
	ID          int64  `json:"id"`
	ReviewID    int64  `json:"review_id"`
	ReviewTitle string `json:"review_title,omitempty"`
	UserID      int64  `json:"user_id"`
	UserName    string `json:"user_name"`
	Kind        string `json:"kind"`
	CommentID   int64  `json:"comment_id"`
	TaskID      int64  `json:"task_id"`
	RevisionID  int64  `json:"revision_id"`
	Page        int    `json:"page"`
	Summary     string `json:"summary"`
	CreatedAt   string `json:"created_at"`
}

// ReadingSession is one sitting with the document.
type ReadingSession struct {
	ID            int64  `json:"id"`
	ReviewID      int64  `json:"review_id"`
	UserID        int64  `json:"user_id"`
	RevisionID    int64  `json:"revision_id"`
	StartedAt     string `json:"started_at"`
	LastAt        string `json:"last_at"`
	ActiveSeconds int    `json:"active_seconds"`
}

// PageTime is how long one person spent on one page of one revision.
type PageTime struct {
	RevisionID int64  `json:"revision_id"`
	UserID     int64  `json:"user_id"`
	Page       int    `json:"page"`
	Seconds    int    `json:"seconds"`
	FirstAt    string `json:"first_at"`
	LastAt     string `json:"last_at"`
}

// Settings are preferences stored as key/value pairs. Title is box-wide (admin
// only); DateFormat and TimeFormat are personal (user_settings), falling back
// to the box value.
type Settings struct {
	Title         string `json:"title"`
	DateFormat    string `json:"date_format"` // auto | dmy-dot | dmy-slash | mdy-slash | ymd | dmmmy | mmmdy
	TimeFormat    string `json:"time_format"` // auto | 24h | 12h
	SetupComplete bool   `json:"setup_complete"`
}

// Date and time formats the UI can use ("auto" = the browser's locale).
var DateFormats = []string{"auto", "dmy-dot", "dmy-slash", "mdy-slash", "ymd", "dmmmy", "mmmdy"}
var TimeFormats = []string{"auto", "24h", "12h"}

// Valid values for the enums the API accepts.
var (
	Roles         = []string{RoleOwner, RoleApprover, RoleReviewer, RoleObserver}
	CommentKinds  = []string{"comment", "question", "issue"}
	Severities    = []string{"minor", "major", "critical"}
	CommentStates = []string{"open", "resolved", "rejected"}
	Verdicts      = []string{"approved", "changes", "rejected"}
)

// OneOf reports whether v is in list.
func OneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ValidDateFormat / ValidTimeFormat report whether v is a known format id.
func ValidDateFormat(v string) bool { return OneOf(v, DateFormats) }
func ValidTimeFormat(v string) bool { return OneOf(v, TimeFormats) }
