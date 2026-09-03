package commands

import (
	"context"
	"fmt"

	"github-webhook/internal/cache"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v91/github"
)

type ReplyHandler struct {
	DB            *db.DB
	ClientFactory *github.ClientFactory
	EncryptionKey string
	ContextCache  *cache.Cache[string, models.MessageContext]
}

func NewReplyHandler(database *db.DB, factory *github.ClientFactory, key string, ctxCache *cache.Cache[string, models.MessageContext]) *ReplyHandler {
	return &ReplyHandler{
		DB:            database,
		ClientFactory: factory,
		EncryptionKey: key,
		ContextCache:  ctxCache,
	}
}

func (h *ReplyHandler) HandleReply(c *gotdbot.Client, msg *gotdbot.Message) error {
	replyToID := msg.ReplyToMessageID()
	if replyToID == 0 {
		return nil
	}

	key := fmt.Sprintf("%d:%d", msg.ChatId, replyToID)
	mContext, found := h.ContextCache.Get(key)
	if !found {
		return nil
	}

	commentBody := msg.GetText()
	senderID := msg.SenderID()
	user, err := h.DB.GetUserByTelegramID(context.Background(), senderID)
	if err != nil || user.EncryptedOAuthToken == "" {
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = msg.ReplyText(c, "Auth error. Reconnect via /connect", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		fmt.Printf("Failed to create GitHub client: %v\n", err)
		return nil
	}

	if mContext.Type == "pr_review_comment" && mContext.CommentID != 0 {
		comment := gh.CreatePullRequestCommentRequest{
			Body:      commentBody,
			InReplyTo: &mContext.CommentID,
		}
		_, _, err = client.PullRequests.CreateComment(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, comment)
	} else {
		comment := gh.IssueCommentRequest{Body: commentBody}
		_, _, err = client.Issues.CreateComment(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, comment)
	}

	if err != nil {
		fmt.Printf("Failed to post comment to %s/%s#%d: %v\n", mContext.Owner, mContext.Repo, mContext.IssueNumber, err)
		return nil
	}

	return nil
}
