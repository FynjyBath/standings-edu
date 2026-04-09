package domain

import (
	"encoding/json"
	"strings"
)

type Account struct {
	Site      string `json:"site"`
	AccountID string `json:"account_id"`
}

type Student struct {
	FullName   string    `json:"full_name"`
	ID         string    `json:"id"`
	PublicName string    `json:"public_name"`
	Accounts   []Account `json:"accounts"`
}

type Subcontest struct {
	Title string   `json:"title"`
	Tasks []string `json:"tasks"`
}

type Contest struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Olympiad       bool            `json:"olympiad"`
	ContestType    string          `json:"contest_type,omitempty"`
	Provider       string          `json:"provider,omitempty"`
	ProviderConfig json.RawMessage `json:"provider_config,omitempty"`
	Subcontests    []Subcontest    `json:"subcontests"`
}

const (
	ContestTypeTasks    = "tasks"
	ContestTypeProvider = "provider"
)

func (c Contest) TypeOrDefault() string {
	typ := strings.ToLower(strings.TrimSpace(c.ContestType))
	if typ == "" {
		return ContestTypeTasks
	}
	return typ
}

type GroupFile struct {
	Title      string   `json:"title"`
	Update     *bool    `json:"update,omitempty"`
	StudentIDs []string `json:"student_ids"`
}

type GroupDefinition struct {
	Slug       string
	Title      string
	Update     bool
	StudentIDs []string
	Contests   []GroupContestRef
}

type GroupContestRef struct {
	ID     string
	Update bool
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
	StudentID      string   `json:"student_id"`
	PublicName     string   `json:"public_name"`
	Place          string   `json:"place,omitempty"`
	Penalty        *int     `json:"penalty,omitempty"`
	ProviderStatus string   `json:"provider_status,omitempty"`
	SolvedCount    int      `json:"solved_count"`
	TotalScore     int      `json:"total_score,omitempty"`
	Statuses       []string `json:"statuses"`
	Scores         []*int   `json:"scores,omitempty"`
}

type GeneratedContestStandings struct {
	ID          string                `json:"id"`
	Title       string                `json:"title"`
	Olympiad    bool                  `json:"olympiad"`
	ContestType string                `json:"contest_type,omitempty"`
	Subcontests []GeneratedSubcontest `json:"subcontests"`
	Tasks       []GeneratedTask       `json:"tasks"`
	Rows        []GeneratedRow        `json:"rows"`
}

type GeneratedGroupStandings struct {
	GroupSlug  string                      `json:"group_slug"`
	GroupTitle string                      `json:"group_title"`
	Contests   []GeneratedContestStandings `json:"contests"`
}
