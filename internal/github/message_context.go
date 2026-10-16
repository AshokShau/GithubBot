package github

import (
	"strings"

	"github-webhook/internal/models"

	"github.com/google/go-github/v92/github"
)

func messageContext(event any) (models.MessageContext, bool) {
	var c models.MessageContext
	var repo *github.Repository
	switch e := event.(type) {
	case *github.PullRequestEvent:
		repo = e.GetRepo()
		c.Type = "pr"
		c.IssueNumber = e.GetPullRequest().GetNumber()
	case *github.PullRequestTargetEvent:
		repo = e.GetRepo()
		c.Type = "pr"
		c.IssueNumber = e.GetPullRequest().GetNumber()
	case *github.PullRequestReviewEvent:
		repo = e.GetRepo()
		c.Type = "pr_review"
		c.IssueNumber = e.GetPullRequest().GetNumber()
	case *github.PullRequestReviewCommentEvent:
		repo = e.GetRepo()
		c.Type = "pr_review_comment"
		c.IssueNumber = e.GetPullRequest().GetNumber()
		c.CommentID = e.GetComment().GetID()
	case *github.PullRequestReviewThreadEvent:
		repo = e.GetRepo()
		c.Type = "pr"
		c.IssueNumber = e.GetPullRequest().GetNumber()
	case *github.IssuesEvent:
		repo = e.GetRepo()
		c.Type = "issue"
		c.IssueNumber = e.GetIssue().GetNumber()
	case *github.IssueCommentEvent:
		repo = e.GetRepo()
		c.Type = "issue_comment"
		c.IssueNumber = e.GetIssue().GetNumber()
		c.CommentID = e.GetComment().GetID()
		if e.GetIssue().GetPullRequestLinks() != nil {
			c.Type = "pr_comment"
		}
	case *github.WorkflowRunEvent:
		repo = e.GetRepo()
		c.Type = "workflow"
		c.RunID = e.GetWorkflowRun().GetID()
	case *github.WorkflowJobEvent:
		repo = e.GetRepo()
		c.Type = "workflow"
		c.RunID = e.GetWorkflowJob().GetRunID()
	case *github.DiscussionEvent:
		repo = e.GetRepo()
		c.Type = "discussion"
		c.IssueNumber = e.GetDiscussion().GetNumber()
		c.DiscussionNodeID = e.GetDiscussion().GetNodeID()
	case *github.DiscussionCommentEvent:
		repo = e.GetRepo()
		c.Type = "discussion_comment"
		c.IssueNumber = e.GetDiscussion().GetNumber()
		c.DiscussionNodeID = e.GetDiscussion().GetNodeID()
		c.CommentID = e.GetComment().GetID()
		c.CommentNodeID = e.GetComment().GetNodeID()
	default:
		return c, false
	}
	c.Owner = repo.GetOwner().GetLogin()
	c.Repo = repo.GetName()
	if owner, name, ok := strings.Cut(repo.GetFullName(), "/"); ok {
		c.Owner = owner
		c.Repo = name
	}
	return c, c.Owner != "" && c.Repo != "" && (c.IssueNumber > 0 || c.RunID > 0)
}
