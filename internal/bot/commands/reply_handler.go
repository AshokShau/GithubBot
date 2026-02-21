package commands

import (
	"context"
	"fmt"

	"github-webhook/internal/cache"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	gh "github.com/google/go-github/v83/github"
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

func (h *ReplyHandler) HandleReply(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage == nil {
		return nil
	}

	key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
	mContext, found := h.ContextCache.Get(key)
	if !found {
		return nil
	}

	commentBody := msg.Text
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		// _, _ = msg.Reply(b, "Please connect GitHub account via /connect first.", nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = msg.Reply(b, "Auth error. Reconnect via /connect", nil)
		return nil
	}

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	if mContext.Type == "pr_review_comment" && mContext.CommentID != 0 {
		comment := &gh.PullRequestComment{
			Body:      &commentBody,
			InReplyTo: &mContext.CommentID,
		}
		_, _, err = client.PullRequests.CreateComment(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, comment)
	} else {
		comment := &gh.IssueComment{Body: &commentBody}
		_, _, err = client.Issues.CreateComment(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, comment)
	}

	if err != nil {
		fmt.Printf("Failed to post comment to %s/%s#%d: %v\n", mContext.Owner, mContext.Repo, mContext.IssueNumber, err)
		return nil
	}

	return nil
}
