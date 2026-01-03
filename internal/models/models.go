package models

import (
	"go.mongodb.org/mongo-driver/v2/bson"
)

// User represents a Telegram user linked to a GitHub account
type User struct {
	ID                  int64    `bson:"_id" json:"telegram_id"`
	GitHubUserID        int64    `bson:"github_user_id" json:"github_user_id"`
	GitHubUsername      string   `bson:"github_username" json:"github_username"`
	EncryptedOAuthToken string   `bson:"encrypted_oauth_token" json:"-"`
	Scopes              []string `bson:"scopes" json:"scopes"`
}

// RepoLink represents a link to a GitHub repository within a chat
type RepoLink struct {
	RepoFullName string `bson:"repo_full_name" json:"repo_full_name"`
	WebhookID    int64  `bson:"webhook_id,omitempty" json:"webhook_id,omitempty"`
}

// Chat represents a Telegram chat (group, channel, or private)
type Chat struct {
	ID       int64      `bson:"_id" json:"chat_id"`
	ChatType string     `bson:"chat_type" json:"chat_type"`
	Title    string     `bson:"title" json:"title"`
	Links    []RepoLink `bson:"links" json:"links"`
}

// Repository represents a GitHub repository where the App is installed
type Repository struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	Owner          string        `bson:"owner" json:"owner"`
	Name           string        `bson:"name" json:"name"`
	FullName       string        `bson:"full_name" json:"full_name"`
	InstallationID int64         `bson:"installation_id" json:"installation_id"`
}

// PRActionContext stores metadata for PR actions to avoid large callback payloads
type PRActionContext struct {
	Owner    string
	Repo     string
	PRNumber int
}
