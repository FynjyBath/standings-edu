package sites

import "context"

type SiteClient interface {
	FetchUserStatuses(ctx context.Context, accountID string) (solved []string, attempted []string, err error)
}
