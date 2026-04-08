package tasksbased

import "context"

type TaskResult struct {
	TaskURL   string
	Attempted bool
	Solved    bool
	Score     *int
}

type SiteClient interface {
	FetchUserResults(ctx context.Context, accountID string) ([]TaskResult, error)
	SupportsTaskScores() bool
	MatchTaskURL(taskURL string) bool
}
