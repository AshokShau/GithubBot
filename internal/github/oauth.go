package github

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github-webhook/internal/config"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

type OAuth struct {
	Config      *config.Config
	OAuthConfig *oauth2.Config
}

func NewOAuth(cfg *config.Config) *OAuth {
	return &OAuth{
		Config: cfg,
		OAuthConfig: &oauth2.Config{
			ClientID:     cfg.GitHubClientID,
			ClientSecret: cfg.GitHubClientSecret,
			Endpoint:     github.Endpoint,
			Scopes:       []string{"repo", "admin:repo_hook", "read:user"},
			RedirectURL:  cfg.TelegramWebhookURL + "/oauth/callback",
		},
	}
}

func (o *OAuth) GetLoginURL(state string) string {
	return o.OAuthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline)
}

func (o *OAuth) ExchangeCode(ctx context.Context, code string) (*oauth2.Token, error) {
	return o.OAuthConfig.Exchange(ctx, code)
}

func GenerateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
