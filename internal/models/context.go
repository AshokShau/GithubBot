package models

import "time"

// MessageContext stores the GitHub context associated with a Telegram message ID
type MessageContext struct {
	Owner            string
	Repo             string
	IssueNumber      int
	CommentID        int64
	RunID            int64
	DiscussionNodeID string
	CommentNodeID    string
	Type             string
}

type OAuthState struct {
	TelegramID int64
	CreatedAt  time.Time
}
