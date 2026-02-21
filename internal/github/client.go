package github

import (
	"context"

	"github.com/google/go-github/v83/github"
	"golang.org/x/oauth2"
)

type ClientFactory struct {
}

func NewClientFactory() *ClientFactory {
	return &ClientFactory{}
}

// GetUserClient returns a GitHub client authenticated as a specific User (via OAuth token)
func (f *ClientFactory) GetUserClient(ctx context.Context, accessToken string) *github.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})
	tc := oauth2.NewClient(ctx, ts)
	return github.NewClient(tc)
}
