package sites

import (
	"context"
	"time"
)

type InformaticsStubClient struct{}

func NewInformaticsStubClient() *InformaticsStubClient {
	return &InformaticsStubClient{}
}

func (c *InformaticsStubClient) FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error) {
	select {
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	case <-time.After(20 * time.Millisecond):
	}

	switch accountID {
	case "436037":
		return []string{
				"https://site1.example/problem/1",
				"https://site2.example/tasks/ABC/",
			}, []string{
				"https://site1.example/problem/2#try",
				"https://site2.example/tasks/def",
			}, nil
	case "100500":
		return []string{
				"https://site1.example/problem/2",
				"https://site3.example/olymp/str/1",
			}, []string{
				"https://site2.example/tasks/ghi/",
				"https://site3.example/olymp/str/2",
			}, nil
	case "777":
		return nil, []string{
			"https://site4.example/dp/A",
		}, nil
	default:
		return nil, nil, nil
	}
}
