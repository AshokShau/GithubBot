package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github-webhook/internal/cache"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v92/github"
	"go.mongodb.org/mongo-driver/v2/mongo"
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
	if !found || mContext.Type == "workflow" {
		return nil
	}

	if (mContext.Type == "discussion" || mContext.Type == "discussion_comment") && mContext.DiscussionNodeID == "" {
		_, err := msg.ReplyText(c, "This discussion cannot be updated from this notification. Open it on GitHub to comment.", nil)
		return err
	}
	commentBody := strings.TrimSpace(msg.GetText())
	if commentBody == "" {
		_, err := msg.ReplyText(c, "A GitHub comment needs text. Reply with a text message.", nil)
		return err
	}
	senderID := msg.SenderID()
	user, err := h.DB.GetUserByTelegramID(context.Background(), senderID)
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		_, _ = msg.ReplyText(c, "Comment was not posted.\n\n"+utils.ConnectionError(err), nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = msg.ReplyText(c, "GitHub authorization is unavailable. Use /connect to reconnect.", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		c.Logger.Warnf("Could not post the comment: %s", utils.ErrorDetails(err))
		_, _ = msg.ReplyText(c, utils.OperationError("Could not post the comment", err), nil)
		return nil
	}

	if mContext.DiscussionNodeID != "" {
		query := `mutation($id: ID!, $body: String!) { addDiscussionComment(input:{discussionId:$id,body:$body}) { comment { id } } }`
		req, requestErr := client.NewRequest(context.Background(), "POST", "graphql", map[string]any{"query": query, "variables": map[string]any{"id": mContext.DiscussionNodeID, "body": commentBody}})
		if requestErr != nil {
			err = requestErr
		} else {
			err = doGraphQL(context.Background(), client, req, nil)
		}
	} else if mContext.Type == "pr_review_comment" && mContext.CommentID != 0 {
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
		c.Logger.Warnf("Could not post the comment: %s", utils.ErrorDetails(err))
		_, _ = msg.ReplyText(c, utils.OperationError("Could not post the comment", err), nil)
		return nil
	}

	return nil
}
