package domain

import (
	"encoding/json"
	"fmt"
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
	Groups     []string  `json:"groups,omitempty"`
}

type Subcontest struct {
	Title string   `json:"title"`
	Tasks []string `json:"tasks"`
}

type ContestMaterial struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type OlympiadMode string

const (
	OlympiadModeEdu OlympiadMode = "edu"
	OlympiadModeIOI OlympiadMode = "ioi"
)

func (m OlympiadMode) Normalized() OlympiadMode {
	switch strings.ToLower(strings.TrimSpace(string(m))) {
	case "", string(OlympiadModeEdu):
		return OlympiadModeEdu
	case string(OlympiadModeIOI):
		return OlympiadModeIOI
	default:
		return OlympiadModeEdu
	}
}

func (m OlympiadMode) IsIOI() bool {
	return m.Normalized() == OlympiadModeIOI
}

func (m OlympiadMode) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(m.Normalized()))
}

func (m *OlympiadMode) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		switch strings.ToLower(strings.TrimSpace(asString)) {
		case "", string(OlympiadModeEdu):
			*m = OlympiadModeEdu
			return nil
		case string(OlympiadModeIOI):
			*m = OlympiadModeIOI
			return nil
		default:
			return fmt.Errorf("olympiad must be %q or %q", OlympiadModeEdu, OlympiadModeIOI)
		}
	}

	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		if asBool {
			*m = OlympiadModeIOI
		} else {
			*m = OlympiadModeEdu
		}
		return nil
	}

	return fmt.Errorf("olympiad must be string (%q/%q) or bool", OlympiadModeEdu, OlympiadModeIOI)
}

type Contest struct {
	ID             string            `json:"id"`
	Title          string            `json:"title"`
	Olympiad       OlympiadMode      `json:"olympiad"`
	ContestType    string            `json:"contest_type,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	ProviderConfig json.RawMessage   `json:"provider_config,omitempty"`
	Materials      []ContestMaterial `json:"materials,omitempty"`
	Subcontests    []Subcontest      `json:"subcontests"`
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

func NormalizeContestMaterials(materials []ContestMaterial) []ContestMaterial {
	if len(materials) == 0 {
		return nil
	}

	out := make([]ContestMaterial, 0, len(materials))
	for _, material := range materials {
		url := strings.TrimSpace(material.URL)
		if url == "" {
			continue
		}

		title := strings.TrimSpace(material.Title)
		if title == "" {
			title = url
		}

		out = append(out, ContestMaterial{
			Title: title,
			URL:   url,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type GroupFile struct {
	Title      string   `json:"title"`
	FormLink   string   `json:"form_link,omitempty"`
	Update     *bool    `json:"update,omitempty"`
	StudentIDs []string `json:"student_ids"`
}

type GroupDefinition struct {
	Slug       string
	Title      string
	FormLink   string
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
	Olympiad    OlympiadMode          `json:"olympiad"`
	ContestType string                `json:"contest_type,omitempty"`
	Materials   []ContestMaterial     `json:"materials,omitempty"`
	Subcontests []GeneratedSubcontest `json:"subcontests"`
	Tasks       []GeneratedTask       `json:"tasks"`
	Rows        []GeneratedRow        `json:"rows"`
}

type GeneratedGroupStandings struct {
	GroupSlug  string                      `json:"group_slug"`
	GroupTitle string                      `json:"group_title"`
	FormLink   string                      `json:"form_link,omitempty"`
	Contests   []GeneratedContestStandings `json:"contests"`
}
