package domain

type Account struct {
	Site      string `json:"site"`
	AccountID string `json:"account_id"`
}

type Student struct {
	ID       string    `json:"id"`
	FullName string    `json:"full_name"`
	Accounts []Account `json:"accounts"`
}

type Subcontest struct {
	Title string   `json:"title"`
	Tasks []string `json:"tasks"`
}

type Contest struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Subcontests []Subcontest `json:"subcontests"`
}

type GroupFile struct {
	Title      string   `json:"title"`
	StudentIDs []string `json:"student_ids"`
}

type GroupDefinition struct {
	Slug       string
	Title      string
	StudentIDs []string
	ContestIDs []string
}

type SourceData struct {
	Students map[string]Student
	Contests map[string]Contest
	Groups   []GroupDefinition
}

const (
	TaskStatusSolved    = "solved"
	TaskStatusAttempted = "attempted"
	TaskStatusNone      = "none"
)

type GeneratedGroupMeta struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

type GeneratedTask struct {
	Label         string `json:"label"`
	URL           string `json:"url"`
	NormalizedURL string `json:"normalized_url"`
}

type GeneratedSubcontest struct {
	Title     string          `json:"title"`
	TaskCount int             `json:"task_count"`
	Tasks     []GeneratedTask `json:"tasks"`
}

type GeneratedRow struct {
	StudentID   string   `json:"student_id"`
	FullName    string   `json:"full_name"`
	SolvedCount int      `json:"solved_count"`
	Statuses    []string `json:"statuses"`
}

type GeneratedContestStandings struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Subcontests []GeneratedSubcontest `json:"subcontests"`
	Tasks       []GeneratedTask       `json:"tasks"`
	Rows        []GeneratedRow        `json:"rows"`
}

type GeneratedGroupStandings struct {
	GroupSlug  string                      `json:"group_slug"`
	GroupTitle string                      `json:"group_title"`
	Contests   []GeneratedContestStandings `json:"contests"`
}

type GeneratedOverallRow struct {
	StudentID    string `json:"student_id"`
	FullName     string `json:"full_name"`
	SolvedBySite []int  `json:"solved_by_site"`
	TotalSolved  int    `json:"total_solved"`
}

type GeneratedOverallStandings struct {
	Sites []string              `json:"sites"`
	Rows  []GeneratedOverallRow `json:"rows"`
}
