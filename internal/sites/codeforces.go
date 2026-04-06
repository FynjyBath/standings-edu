package sites

import (
	"context"
	"time"
)

type CodeforcesStubClient struct{}

func NewCodeforcesStubClient() *CodeforcesStubClient {
	return &CodeforcesStubClient{}
}

func (c *CodeforcesStubClient) FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}

	switch accountID {
	case "tourist":
		return []string{
				"https://site4.example/dp/a",
				"https://site4.example/dp/b/",
			}, []string{
				"https://site3.example/olymp/str/2",
			}, nil
	case "schoolboy":
		return []string{
				"https://site1.example/problem/1/",
			}, []string{
				"https://site2.example/tasks/ghi",
				"https://site4.example/dp/B",
			}, nil
	default:
		return nil, nil, nil
	}
}
