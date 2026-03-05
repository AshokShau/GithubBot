package github

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/google/go-github/v84/github"
)

type WebhookServer struct {
	Config       *config.Config
	DB           *db.DB
	Bot          *gotgbot.Bot
	ContextCache *cache.Cache[string, models.MessageContext]  // Key: "chat_id:message_id"
	ActionCache  *cache.Cache[string, models.PRActionContext] // Key: UUID
}

func NewWebhookServer(cfg *config.Config, database *db.DB, bot *gotgbot.Bot, ctxCache *cache.Cache[string, models.MessageContext], actionCache *cache.Cache[string, models.PRActionContext]) *WebhookServer {
	return &WebhookServer{
		Config:       cfg,
		DB:           database,
		Bot:          bot,
		ContextCache: ctxCache,
		ActionCache:  actionCache,
	}
}

func (s *WebhookServer) Handler(w http.ResponseWriter, r *http.Request) {
	//log.Printf("Received webhook request: %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
	// Path: /webhook/<token>
	var chatID int64
	path := r.URL.Path
	if strings.HasPrefix(path, "/webhook/") && len(path) > 9 {
		token := path[9:] // strip "/webhook/"
		decrypted, err := utils.Decrypt(token, s.Config.EncryptionKey)
		if err == nil {
			id, err := strconv.ParseInt(decrypted, 10, 64)
			if err == nil {
				chatID = id
				// log.Printf("Decrypted Chat ID from token: %d", chatID)
			} else {
				log.Printf("Failed to parse decrypted token as int64: %v", err)
			}
		} else {
			log.Printf("Failed to decrypt webhook token: %v", err)
		}
	}

	if chatID == 0 {
		log.Printf("Error: Valid webhook token required.")
		http.Error(w, "Unauthorized: Token required", http.StatusUnauthorized)
		return
	}

	payload, err := github.ValidatePayload(r, []byte(s.Config.GitHubWebhookSecret))
	if err != nil {
		log.Printf("Error: Webhook signature validation failed. Ensure GITHUB_WEBHOOK_SECRET matches. Error: %v", err)
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		log.Printf("Error: Webhook parsing failed: %v", err)
		http.Error(w, "Parse error", http.StatusInternalServerError)
		return
	}

	var hookID int64
	if idStr := r.Header.Get("X-GitHub-Hook-ID"); idStr != "" {
		hookID, _ = strconv.ParseInt(idStr, 10, 64)
	}

	go s.processEvent(event, chatID, hookID)
	w.WriteHeader(http.StatusOK)
}

func (s *WebhookServer) processEvent(event interface{}, chatID int64, hookID int64) {
	if e, ok := event.(*github.RepositoryEvent); ok && e.GetAction() == "renamed" {
		newFullName := e.GetRepo().GetFullName()
		if newFullName != "" && hookID != 0 {
			err := s.DB.UpdateRepoLinkName(context.Background(), chatID, hookID, newFullName)
			if err != nil {
				log.Printf("Failed to update repo name for chat %d: %v", chatID, err)
			} else {
				log.Printf("Updated repo name to %s for chat %d", newFullName, chatID)
			}
		}
	}

	msg, markup := s.formatMessage(event)
	if msg == "" {
		return
	}

	msg = normalizeMessage(msg)

	opts := &gotgbot.SendMessageOpts{
		ParseMode: "MarkdownV2",
		LinkPreviewOptions: &gotgbot.LinkPreviewOptions{
			IsDisabled: true,
		},
		ReplyMarkup: markup,
	}

	sentMsg, err := s.Bot.SendMessage(chatID, msg, opts)
	if err != nil {
		log.Printf("Error sending message to chat %d: %v", chatID, err)
		return
	}

	s.storeMessageContext(sentMsg.MessageId, chatID, event)
}

// normalizeMessage trims trailing spaces on each line, collapses 3+ consecutive newlines into 2
func normalizeMessage(s string) string {
	if s == "" {
		return s
	}

	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, " \t")
	}
	out := strings.Join(lines, "\n")

	re := regexp.MustCompile(`\n{3,}`)
	out = re.ReplaceAllString(out, "\n\n")

	out = strings.TrimSpace(out)
	return out
}

func (s *WebhookServer) storeMessageContext(messageID int64, chatID int64, event interface{}) {
	key := fmt.Sprintf("%d:%d", chatID, messageID)
	var ctx models.MessageContext

	switch e := event.(type) {
	case *github.PullRequestEvent:
		ctx = models.MessageContext{
			Owner:       e.GetRepo().GetOwner().GetLogin(),
			Repo:        e.GetRepo().GetName(),
			IssueNumber: e.GetPullRequest().GetNumber(),
			Type:        "pr",
		}
	case *github.IssuesEvent:
		ctx = models.MessageContext{
			Owner:       e.GetRepo().GetOwner().GetLogin(),
			Repo:        e.GetRepo().GetName(),
			IssueNumber: e.GetIssue().GetNumber(),
			Type:        "issue",
		}
	case *github.IssueCommentEvent:
		ctx = models.MessageContext{
			Owner:       e.GetRepo().GetOwner().GetLogin(),
			Repo:        e.GetRepo().GetName(),
			IssueNumber: e.GetIssue().GetNumber(),
			CommentID:   e.GetComment().GetID(),
			Type:        "issue_comment",
		}
	case *github.PullRequestReviewEvent:
		ctx = models.MessageContext{
			Owner:       e.GetRepo().GetOwner().GetLogin(),
			Repo:        e.GetRepo().GetName(),
			IssueNumber: e.GetPullRequest().GetNumber(),
			Type:        "pr_review",
		}
	case *github.PullRequestReviewCommentEvent:
		ctx = models.MessageContext{
			Owner:       e.GetRepo().GetOwner().GetLogin(),
			Repo:        e.GetRepo().GetName(),
			IssueNumber: e.GetPullRequest().GetNumber(),
			CommentID:   e.GetComment().GetID(),
			Type:        "pr_review_comment",
		}
	default:
		return
	}

	s.ContextCache.Set(key, ctx, 48*time.Hour)
}

func (s *WebhookServer) formatMessage(event interface{}) (string, *gotgbot.InlineKeyboardMarkup) {
	switch e := event.(type) {
	case *github.PushEvent:
		return FormatPushEvent(e)
	case *github.PullRequestEvent:
		return FormatPullRequestEvent(e)
	case *github.IssuesEvent:
		return FormatIssuesEvent(e)
	case *github.PingEvent:
		return FormatPingEvent(e)
	case *github.PullRequestReviewEvent:
		return FormatPullRequestReviewEvent(e)
	case *github.PullRequestReviewCommentEvent:
		return FormatPullRequestReviewCommentEvent(e)
	case *github.RepositoryEvent:
		return FormatRepositoryEvent(e)
	case *github.RepositoryDispatchEvent:
		return FormatRepositoryDispatchEvent(e)
	case *github.OrganizationEvent:
		return FormatOrganizationEvent(e)
	case *github.OrgBlockEvent:
		return FormatOrgBlockEvent(e)
	case *github.CheckRunEvent:
		return FormatCheckRunEvent(e)
	case *github.CheckSuiteEvent:
		return FormatCheckSuiteEvent(e)
	case *github.WorkflowRunEvent:
		return FormatWorkflowRunEvent(e)
	case *github.WorkflowJobEvent:
		return FormatWorkflowJobEvent(e)
	case *github.DeploymentEvent:
		return FormatDeploymentEvent(e)
	case *github.DeploymentStatusEvent:
		return FormatDeploymentStatusEvent(e)
	case *github.SecurityAdvisoryEvent:
		return FormatSecurityAdvisoryEvent(e)
	case *github.RepositoryVulnerabilityAlertEvent:
		return FormatRepositoryVulnerabilityAlertEvent(e)
	case *github.BranchProtectionRuleEvent:
		return FormatBranchProtectionRuleEvent(e)
	case *github.BranchProtectionConfigurationEvent:
		return FormatBranchProtectionConfigurationEvent(e)
	case *github.ContentReferenceEvent:
		return FormatContentReferenceEvent(e)
	case *github.CustomPropertyEvent:
		return FormatCustomPropertyEvent(e)
	case *github.CustomPropertyValuesEvent:
		return FormatCustomPropertyValuesEvent(e)
	case *github.DependabotAlertEvent:
		return FormatDependabotAlertEvent(e)
	case *github.DeploymentProtectionRuleEvent:
		return FormatDeploymentProtectionRuleEvent(e)
	case *github.DeploymentReviewEvent:
		return FormatDeploymentReviewEvent(e)
	case *github.DiscussionCommentEvent:
		return FormatDiscussionCommentEvent(e)
	case *github.DiscussionEvent:
		return FormatDiscussionEvent(e)
	case *github.GitHubAppAuthorizationEvent:
		return FormatGitHubAppAuthorizationEvent(e)
	case *github.InstallationRepositoriesEvent:
		return FormatInstallationRepositoriesEvent(e)
	case *github.InstallationTargetEvent:
		return FormatInstallationTargetEvent(e)
	case *github.MergeGroupEvent:
		return FormatMergeGroupEvent(e)
	case *github.PersonalAccessTokenRequestEvent:
		return FormatPersonalAccessTokenRequestEvent(e)
	case *github.ProjectV2Event:
		return FormatProjectV2Event(e)
	case *github.ProjectV2ItemEvent:
		return FormatProjectV2ItemEvent(e)
	case *github.PullRequestReviewThreadEvent:
		return FormatPullRequestReviewThreadEvent(e)
	case *github.PullRequestTargetEvent:
		return FormatPullRequestTargetEvent(e)
	case *github.RegistryPackageEvent:
		return FormatRegistryPackageEvent(e)
	case *github.RepositoryImportEvent:
		return FormatRepositoryImportEvent(e)
	case *github.RepositoryRulesetEvent:
		return FormatRepositoryRulesetEvent(e)
	case *github.SecretScanningAlertEvent:
		return FormatSecretScanningAlertEvent(e)
	case *github.SecretScanningAlertLocationEvent:
		return FormatSecretScanningAlertLocationEvent(e)
	case *github.SecurityAndAnalysisEvent:
		return FormatSecurityAndAnalysisEvent(e)
	case *github.SponsorshipEvent:
		return FormatSponsorshipEvent(e)
	case *github.UserEvent:
		return FormatUserEvent(e)
	case *github.MembershipEvent:
		return FormatMembershipEvent(e)
	case *github.MilestoneEvent:
		return FormatMilestoneEvent(e)
	case *github.CommitCommentEvent:
		return FormatCommitCommentEvent(e)
	case *github.ForkEvent:
		return FormatForkEvent(e)
	case *github.ReleaseEvent:
		return FormatReleaseEvent(e)
	case *github.StarEvent:
		return FormatStarEvent(e)
	case *github.WatchEvent:
		return FormatWatchEvent(e)
	case *github.LabelEvent:
		return FormatLabelEvent(e)
	case *github.MarketplacePurchaseEvent:
		return FormatMarketplacePurchaseEvent(e)
	case *github.PageBuildEvent:
		return FormatPageBuildEvent(e)
	case *github.DeployKeyEvent:
		return FormatDeployKeyEvent(e)
	case *github.CreateEvent:
		return FormatCreateEvent(e)
	case *github.DeleteEvent:
		return FormatDeleteEvent(e)
	case *github.IssueCommentEvent:
		return FormatIssueCommentEvent(e)
	case *github.MemberEvent:
		return FormatMemberEvent(e)
	case *github.PublicEvent:
		return FormatPublicEvent(e)
	case *github.StatusEvent:
		return FormatStatusEvent(e)
	case *github.WorkflowDispatchEvent:
		return FormatWorkflowDispatchEvent(e)
	case *github.TeamAddEvent:
		return FormatTeamAddEvent(e)
	case *github.TeamEvent:
		return FormatTeamEvent(e)
	case *github.PackageEvent:
		return FormatPackageEvent(e)
	case *github.GollumEvent:
		return FormatGollumEvent(e)
	case *github.MetaEvent:
		return FormatMetaEvent(e)
	case *github.InstallationEvent:
		return FormatInstallationEvent(e)
	default:
		return "", nil
	}
}
