package commands

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"time"

	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	gh "github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"
	"strings"

	"html"
	"net/http"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/google/go-github/v90/github"
)

type CommandHandler struct {
	Config          *config.Config
	DB              *db.DB
	OAuth           *gh.OAuth
	StateCache      *cache.Cache[string, int64]
	ClientFactory   *gh.ClientFactory
	EncryptionKey   string
	AdminCache      *cache.Cache[int64, []int64]
	ReloadRateLimit *cache.Cache[int64, time.Time]
	ContextCache    *cache.Cache[string, models.MessageContext]
}

func NewCommandHandler(cfg *config.Config, database *db.DB, oauth *gh.OAuth, stateCache *cache.Cache[string, int64], factory *gh.ClientFactory, key string, ctxCache *cache.Cache[string, models.MessageContext], adminCache *cache.Cache[int64, []int64], reloadLimit *cache.Cache[int64, time.Time]) *CommandHandler {
	return &CommandHandler{
		Config:          cfg,
		DB:              database,
		OAuth:           oauth,
		StateCache:      stateCache,
		ClientFactory:   factory,
		EncryptionKey:   key,
		AdminCache:      adminCache,
		ReloadRateLimit: reloadLimit,
		ContextCache:    ctxCache,
	}
}

func (h *CommandHandler) Start(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := `<b>Welcome to the GitHub Bot!</b> 🤖

I can help you manage your GitHub repositories and notifications directly from Telegram.

<b>Get Started:</b>
1. Use /connect to link your GitHub account.
2. Use /addrepo to link a repository and start receiving notifications.
3. Use /settings to customize your notification preferences.

Need help? Type /help for a full list of commands.`
	_, err := ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Connect(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type != gotgbot.ChatTypePrivate {
		_, err := ctx.EffectiveMessage.Reply(b, "⚠️ The /connect command can only be used in a private chat with the bot.", nil)
		return err
	}

	state, err := gh.GenerateState()
	if err != nil {
		return err
	}

	h.StateCache.Set(state, ctx.EffectiveUser.Id, 10*time.Minute)

	url := h.OAuth.GetLoginURL(state)

	msg := fmt.Sprintf("Please [connect your GitHub account](%s) to enable automatic webhook setup and perform actions like approving PRs.", url)
	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
	return err
}

func (h *CommandHandler) AddRepo(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type != gotgbot.ChatTypePrivate && !utils.IsAdmin(b, ctx.EffectiveChat.Id, ctx.EffectiveUser.Id, h.AdminCache) {
		_, err := ctx.EffectiveMessage.Reply(b, "Only admins can add repositories.", nil)
		return err
	}

	args := ctx.Args()
	if len(args) < 2 {
		return h.listUserRepos(b, ctx)
	}

	repoFullName := args[1]
	user, uErr := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if uErr != nil || user.EncryptedOAuthToken == "" {
		msg := fmt.Sprintf("Please [connect your GitHub account](%s) first to link repository %s.", h.OAuth.GetLoginURL("connect"), repoFullName)
		_, _ = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return nil
	}

	token, decErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if decErr != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Auth error. Reconnect via /connect", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to create GitHub client.", nil)
		return nil
	}

	var owner, repo string
	if n := len(repoFullName); n > 0 {
		for i := range n {
			if repoFullName[i] == '/' {
				owner = repoFullName[:i]
				repo = repoFullName[i+1:]
				break
			}
		}
	}

	if owner == "" || repo == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Invalid repository format. Use owner/repo", nil)
		return nil
	}

	_, _, getErr := client.Repositories.Get(context.Background(), owner, repo)
	if getErr != nil {
		if h.handleAuthError(b, ctx, getErr) {
			return nil
		}
		if errResp, ok := errors.AsType[*github.ErrorResponse](getErr); ok && errResp.Response.StatusCode == http.StatusNotFound {
			_, _ = ctx.EffectiveMessage.Reply(b, "❌ <b>Repository not found.</b>\nPlease check the name and ensure you have access.", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Error fetching repository: %v", getErr), nil)
		return nil
	}

	payload := fmt.Sprintf("%d", ctx.EffectiveChat.Id)
	if ctx.EffectiveMessage.MessageThreadId != 0 {
		payload = fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, ctx.EffectiveMessage.MessageThreadId)
	}

	token, encErr := utils.Encrypt(payload, h.EncryptionKey)
	if encErr != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Error generating webhook token.", nil)
		return nil
	}

	webhookURL := fmt.Sprintf("%s/webhook/%s", h.Config.TelegramWebhookURL, token)
	webhookConfig := &github.HookConfig{
		URL:         github.Ptr(webhookURL),
		ContentType: github.Ptr("json"),
		Secret:      github.Ptr(h.Config.GitHubWebhookSecret),
	}

	var defaultEvents []string
	for _, e := range gh.SupportedEvents {
		defaultEvents = append(defaultEvents, e.Name)
	}

	hook := &github.Hook{
		Name:   new("web"),
		Events: defaultEvents,
		Config: webhookConfig,
		Active: new(true),
	}

	createdHook, _, hookErr := client.Repositories.CreateHook(context.Background(), owner, repo, hook)
	if hookErr != nil {
		if h.handleAuthError(b, ctx, hookErr) {
			return nil
		}
		if errResp, ok := errors.AsType[*github.ErrorResponse](hookErr); ok && errResp.Response.StatusCode == http.StatusNotFound {
			safeRepoName := html.EscapeString(repoFullName)
			msg := fmt.Sprintf("❌ <b>Insufficient permissions.</b>\nYou need admin access to repository <b>%s</b> to create webhooks.", safeRepoName)
			_, err := ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return err
		}

		log.Printf("Webhook creation failed for %s: %v", repoFullName, hookErr)
		msg := "⚠️ <b>Webhook creation failed.</b>\nPlease ensure you have admin rights and try again."
		_, err := ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	webhookID := createdHook.GetID()
	link := models.RepoLink{
		RepoFullName: repoFullName,
		WebhookID:    webhookID,
	}

	err = h.DB.AddRepoLink(context.Background(), ctx.EffectiveChat.Id, link)
	if err != nil {
		_, err := ctx.EffectiveMessage.Reply(b, "Error linking repository.", nil)
		return err
	}

	msg := fmt.Sprintf("Repository <b>%s</b> linked successfully!", repoFullName)
	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) listUserRepos(b *gotgbot.Bot, ctx *ext.Context) error {
	return h.sendRepoList(b, ctx, 1)
}

func (h *CommandHandler) sendRepoList(b *gotgbot.Bot, ctx *ext.Context, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Please /connect your GitHub account first to list repositories.", nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Auth error. Reconnect via /connect", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to create GitHub client.", nil)
		return nil
	}

	opts := &github.RepositoryListOptions{
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: github.ListOptions{PerPage: 5, Page: page},
	}

	repos, resp, err := client.Repositories.List(context.Background(), "", opts)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to fetch repositories from GitHub.", nil)
		return nil
	}

	if len(repos) == 0 && page == 1 {
		_, _ = ctx.EffectiveMessage.Reply(b, "No repositories found.", nil)
		return nil
	}

	var kb [][]gotgbot.InlineKeyboardButton
	for _, repo := range repos {
		kb = append(kb, []gotgbot.InlineKeyboardButton{
			{Text: repo.GetFullName(), CallbackData: fmt.Sprintf("c:ar:id:%d", repo.GetID())},
		})
	}

	var navRow []gotgbot.InlineKeyboardButton

	if resp.FirstPage != 0 && resp.PrevPage != 0 {
		navRow = append(navRow, gotgbot.InlineKeyboardButton{Text: "< Prev", CallbackData: fmt.Sprintf("c:ar:pg:%d", resp.PrevPage)})
	}

	startPage := max(page-1, 1)
	endPage := page + 1
	if resp.LastPage != 0 && endPage > resp.LastPage {
		endPage = resp.LastPage
	}

	if resp.LastPage == 0 && resp.NextPage != 0 {
		endPage = resp.NextPage
	}

	for i := startPage; i <= endPage; i++ {
		text := fmt.Sprintf("%d", i)
		if i == page {
			text = fmt.Sprintf("· %d ·", i)
		}
		navRow = append(navRow, gotgbot.InlineKeyboardButton{Text: text, CallbackData: fmt.Sprintf("c:ar:pg:%d", i)})
	}

	if resp.NextPage != 0 {
		navRow = append(navRow, gotgbot.InlineKeyboardButton{Text: "Next >", CallbackData: fmt.Sprintf("c:ar:pg:%d", resp.NextPage)})
	}

	if len(navRow) > 0 {
		kb = append(kb, navRow)
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Select a repository to add (Page %d):", page), &gotgbot.SendMessageOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
	})
	return err
}

func (h *CommandHandler) Settings(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type != gotgbot.ChatTypePrivate && !utils.IsAdmin(b, ctx.EffectiveChat.Id, ctx.EffectiveUser.Id, h.AdminCache) {
		_, err := ctx.EffectiveMessage.Reply(b, "Only admins can modify settings.", nil)
		return err
	}

	links, err := h.DB.GetChatLinks(context.Background(), ctx.EffectiveChat.Id)
	if err != nil {
		return err
	}

	if len(links) == 0 {
		_, err = ctx.EffectiveMessage.Reply(b, "No repositories linked. Use /addrepo first.", nil)
		return err
	}

	var kb [][]gotgbot.InlineKeyboardButton
	for _, l := range links {
		kb = append(kb, []gotgbot.InlineKeyboardButton{
			{Text: l.RepoFullName, CallbackData: fmt.Sprintf("c:r:%s", l.RepoFullName)},
		})
	}

	_, err = ctx.EffectiveMessage.Reply(b, "Select a repository to configure:", &gotgbot.SendMessageOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
	})
	return err
}

func (h *CommandHandler) RemoveRepo(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type != gotgbot.ChatTypePrivate && !utils.IsAdmin(b, ctx.EffectiveChat.Id, ctx.EffectiveUser.Id, h.AdminCache) {
		_, err := ctx.EffectiveMessage.Reply(b, "Only admins can remove repositories.", nil)
		return err
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, err := ctx.EffectiveMessage.Reply(b, "Usage: /removerepo owner/repo", nil)
		return err
	}

	repoFullName := args[1]
	link, err := h.DB.GetRepoLink(context.Background(), ctx.EffectiveChat.Id, repoFullName)
	if err != nil {
		_, err := ctx.EffectiveMessage.Reply(b, "Error finding repository link or not found.", nil)
		return err
	}

	var webhookStatusMsg string

	if link.WebhookID != 0 {
		user, uErr := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
		if uErr != nil || user.EncryptedOAuthToken == "" {
			webhookStatusMsg = "\n\n⚠️ <b>Warning:</b> You are not connected to GitHub. The webhook could not be removed from the repository settings. Please remove it manually."
		} else {
			token, decErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			if decErr != nil {
				webhookStatusMsg = "\n\n⚠️ <b>Warning:</b> Could not decrypt your access token. Webhook not removed from GitHub."
			} else {
				client, err := h.ClientFactory.GetUserClient(context.Background(), token)
				if err != nil {
					webhookStatusMsg = "\n\n⚠️ <b>Warning:</b> Failed to create GitHub client. Webhook not removed."
				} else {
					var owner, repo string
					for i := 0; i < len(repoFullName); i++ {
						if repoFullName[i] == '/' {
							owner = repoFullName[:i]
							repo = repoFullName[i+1:]
							break
						}
					}

					if owner != "" && repo != "" {
						_, err := client.Repositories.DeleteHook(context.Background(), owner, repo, link.WebhookID)
						if err != nil {
							if h.handleAuthError(b, ctx, err) {
								webhookStatusMsg = "\n\n⚠️ <b>Warning:</b> GitHub authentication failed. Webhook not removed."
							} else {
								if errResp, ok := errors.AsType[*github.ErrorResponse](err); ok && errResp.Response.StatusCode == http.StatusNotFound {
								} else {
									webhookStatusMsg = fmt.Sprintf("\n\n⚠️ <b>Warning:</b> Failed to remove webhook from GitHub: %v", err)
								}
							}
						}
					}
				}
			}
		}
	}

	err = h.DB.RemoveRepoLink(context.Background(), ctx.EffectiveChat.Id, repoFullName)
	if err != nil {
		_, err := ctx.EffectiveMessage.Reply(b, "Error removing repository from database.", nil)
		return err
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Repository <b>%s</b> removed successfully.%s", repoFullName, webhookStatusMsg), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Repos(b *gotgbot.Bot, ctx *ext.Context) error {
	links, err := h.DB.GetChatLinks(context.Background(), ctx.EffectiveChat.Id)
	if err != nil {
		return err
	}

	if len(links) == 0 {
		_, err = ctx.EffectiveMessage.Reply(b, "No repositories linked.", nil)
		return err
	}

	var msg strings.Builder
	for _, l := range links {
		msg.WriteString(fmt.Sprintf("• <b>%s</b>\n", l.RepoFullName))
	}

	_, err = ctx.EffectiveMessage.Reply(b, "<b>Linked Repositories:</b>\n"+msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Help(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := `<b>GitHub Bot Commands:</b>

<b>Authentication</b>
/connect - Connect your GitHub account using OAuth
/disconnect - Disconnect your GitHub account
/me - Show your connected GitHub account details

<b>Repository Management</b>
/addrepo owner/repo - Link a repository to this chat
/removerepo owner/repo - Remove a linked repository
/repos - List all linked repositories
/repo [owner/repo] - Show repository information
/star - Star the current repository
/unstar - Remove your star
/watch - Watch repository
/unwatch - Stop watching repository
/fork - Fork repository to your account
/archive - Archive repository
/unarchive - Unarchive repository
/contributors - Show top contributors
/languages - Show language statistics
/branches - List branches
/branch branch-name - Show branch details
/default branch-name - Change default branch

<b>Issues</b>
/issue - Create a new issue
/comment - Add a comment to the replied issue or PR
/close - Close issue or PR (reply to notification)
/reopen - Reopen issue or PR (reply to notification)
/assign @username - Assign a user
/assignme - Assign yourself
/unassign @username - Unassign a user
/label bug - Add label
/label +bug -help wanted - Add/remove multiple labels
/labels - List labels
/milestone v1.0 - Assign milestone
/lock - Lock conversation
/unlock - Unlock conversation
/pin - Pin issue
/unpin - Unpin issue

<b>Pull Requests</b>
/approve - Approve PR (reply to notification)
/requestchanges - Request changes
/merge - Merge PR using default strategy
/merge squash - Squash merge
/merge rebase - Rebase merge
/merge merge - Merge commit
/draft - Convert PR to Draft
/ready - Mark Draft PR as Ready
/checks - Show CI status
/files - List changed files
/diff - Show change summary
/reviews - Show review status
/mergeable - Check merge conflicts
/request @username - Request reviewer

<b>Commits</b>
/commit SHA - Show commit details
/commits - Show recent commits
/compare branch1 branch2 - Compare branches

<b>GitHub Actions</b>
/actions - List recent workflow runs
/run workflow.yml - Run workflow manually
/rerun - Rerun failed workflow (reply to notification)
/cancel - Cancel running workflow (reply to notification)
/logs - Open workflow logs

<b>Releases</b>
/release - Show latest release
/release create v1.0.0 - Create release
/changelog - Generate release notes

<b>Discussions</b>
/discussion - Create discussion
/answered - Mark discussion as answered

<b>Search</b>
/find keyword - Search issues
/pr keyword - Search pull requests
/search keyword - Search repository code

<b>Notifications & Stats</b>
/mute - Mute current thread
/done - Mark notification as done (reply to notification)
/read - Mark notification as read (reply to notification)
/stats - Repository statistics
/activity - Recent repository activity

<b>Settings & Help</b>
/settings - Configure event notifications
/reload - Reload admin cache
/privacy - View the privacy policy
/help - Show this help menu`

	_, err := ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML", LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}})
	return err
}

func (h *CommandHandler) Reload(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type == gotgbot.ChatTypePrivate {
		return nil
	}

	if expiry, ok := h.ReloadRateLimit.Get(ctx.EffectiveChat.Id); ok {
		remaining := time.Until(expiry)
		if remaining > 0 {
			minutes := int(math.Ceil(remaining.Minutes()))
			_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Please wait %d minutes before reloading again.", minutes), nil)
			return nil
		}
	}

	member, err := b.GetChatMember(ctx.EffectiveChat.Id, ctx.EffectiveUser.Id, nil)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to check permissions.", nil)
		return nil
	}

	status := member.GetStatus()
	isAdmin := status == "administrator" || status == "creator"

	if !isAdmin {
		_, _ = ctx.EffectiveMessage.Reply(b, "Only admins can reload the cache.", nil)
		return nil
	}

	h.AdminCache.Delete(ctx.EffectiveChat.Id)
	expiry := time.Now().Add(10 * time.Minute)
	h.ReloadRateLimit.Set(ctx.EffectiveChat.Id, expiry, 10*time.Minute)
	_, err = ctx.EffectiveMessage.Reply(b, "Admin cache reloaded.", nil)
	return err
}

func (h *CommandHandler) Privacy(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := `<b>Privacy Policy</b>

We value your privacy and are committed to protecting your data. This policy outlines how we collect, use, and safeguard your information.

<b>1. Data Collection</b>
• <b>Telegram Data:</b> We store your Telegram User ID, Chat ID, and basic profile information to route notifications and manage permissions.
• <b>GitHub Data:</b> When you connect your account, we securely store your encrypted OAuth token. We also store the names of repositories you link and the Webhook IDs created.
• <b>Events:</b> We process incoming GitHub webhook events (e.g., pushes, issues) to send notifications to your chat. The content of these events is processed in real-time and not permanently stored.

<b>2. Data Usage</b>
• <b>Functionality:</b> Your data is used strictly to provide the bot's services: sending notifications, managing repository links, and verifying permissions.
• <b>Security:</b> Your OAuth tokens are encrypted using AES-GCM before being stored in our database.

<b>3. Data Sharing</b>
• We do <b>not</b> share, sell, or rent your personal data to third parties.
• Data is only shared with GitHub APIs to the extent necessary to perform requested actions (e.g., creating webhooks).

<b>4. Data Control & Rights</b>
• <b>Disconnect:</b> You can unlink your GitHub account at any time, which invalidates the stored token.
• <b>Removal:</b> You can remove repositories using /removerepo. To request full data deletion, please contact the developer or simply block the bot.

<b>5. Contact</b>
If you have questions or concerns, please visit our <a href="https://github.com/AshokShau/GithubBot">GitHub repository</a> or join our <a href="https://t.me/GuardxSupport">Telegram Support Group</a>.`

	_, err := ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML", LinkPreviewOptions: &gotgbot.LinkPreviewOptions{IsDisabled: true}})
	return err
}

func (h *CommandHandler) Logout(b *gotgbot.Bot, ctx *ext.Context) error {
	err := h.DB.ClearUserToken(context.Background(), ctx.EffectiveUser.Id)
	if err != nil {
		_, err = ctx.EffectiveMessage.Reply(b, "Error logging out.", nil)
		return err
	}
	_, err = ctx.EffectiveMessage.Reply(b, "✅ You have been logged out. Use /connect to reconnect.", nil)
	return err
}

func (h *CommandHandler) handleAuthError(b *gotgbot.Bot, ctx *ext.Context, err error) bool {
	if errResp, ok := errors.AsType[*github.ErrorResponse](err); ok {
		if errResp.Response.StatusCode == http.StatusUnauthorized || errResp.Response.StatusCode == http.StatusForbidden {
			_ = h.DB.ClearUserToken(context.Background(), ctx.EffectiveUser.Id)
			msg := "⚠️ <b>GitHub authentication failed.</b>\nIt seems your token has expired or was revoked. Please /connect again."
			_, _ = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return true
		}
	}
	return false
}

func (h *CommandHandler) Close(b *gotgbot.Bot, ctx *ext.Context) error {
	return h.handleIssueAction(b, ctx, "closed")
}

func (h *CommandHandler) Reopen(b *gotgbot.Bot, ctx *ext.Context) error {
	return h.handleIssueAction(b, ctx, "open")
}

func (h *CommandHandler) Approve(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage == nil {
		_, err := msg.Reply(b, "Please use this command in reply to a notification.", nil)
		return err
	}

	key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
	mContext, found := h.ContextCache.Get(key)
	if !found {
		_, err := msg.Reply(b, "Context not found. The message might be too old.", nil)
		return err
	}

	if mContext.Type != "pr" && mContext.Type != "pr_review" {
		_, err := msg.Reply(b, "This command is only for Pull Requests.", nil)
		return err
	}

	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	review := &github.PullRequestReviewRequest{
		Event: new("APPROVE"),
	}
	_, _, err = client.PullRequests.CreateReview(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, review)

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = msg.Reply(b, fmt.Sprintf("Failed to approve: %v", err), nil)
		return nil
	}

	_, err = msg.Reply(b, fmt.Sprintf("✅ PR #%d approved.", mContext.IssueNumber), nil)
	return err
}

func (h *CommandHandler) handleIssueAction(b *gotgbot.Bot, ctx *ext.Context, state string) error {
	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage == nil {
		_, err := msg.Reply(b, "Please use this command in reply to a notification.", nil)
		return err
	}

	key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
	mContext, found := h.ContextCache.Get(key)
	if !found {
		_, err := msg.Reply(b, "Context not found. The message might be too old.", nil)
		return err
	}

	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	req := github.UpdateIssueRequest{State: &state}
	_, _, err = client.Issues.Update(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, req)

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = msg.Reply(b, fmt.Sprintf("Failed to update state: %v", err), nil)
		return nil
	}

	action := "closed"
	if state == "open" {
		action = "reopened"
	}
	_, err = msg.Reply(b, fmt.Sprintf("✅ Issue/PR #%d %s.", mContext.IssueNumber, action), nil)
	return err
}

func (h *CommandHandler) getAuthenticatedClient(b *gotgbot.Bot, ctx *ext.Context) (*github.Client, error) {
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		msg := fmt.Sprintf("Please [connect your GitHub account](%s) first.", h.OAuth.GetLoginURL("connect"))
		_, _ = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "Markdown"})
		return nil, fmt.Errorf("auth required")
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Auth error. Reconnect via /connect", nil)
		return nil, err
	}

	return h.ClientFactory.GetUserClient(context.Background(), token)
}

func (h *CommandHandler) resolveRepoContext(ctx *ext.Context) (owner string, repo string, err error) {
	args := ctx.Args()
	if len(args) > 1 {
		parts := strings.Split(args[1], "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage != nil {
		key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.Owner != "" && mContext.Repo != "" {
			return mContext.Owner, mContext.Repo, nil
		}
	}

	links, dbErr := h.DB.GetChatLinks(context.Background(), ctx.EffectiveChat.Id)
	if dbErr == nil && len(links) == 1 {
		parts := strings.Split(links[0].RepoFullName, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	return "", "", errors.New("could not resolve repository. Please provide `owner/repo` or reply to a notification")
}

func (h *CommandHandler) resolveIssueOrPRNumber(ctx *ext.Context) (int, error) {
	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage != nil {
		key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.IssueNumber != 0 {
			return mContext.IssueNumber, nil
		}
	}

	args := ctx.Args()
	if len(args) > 1 {
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "#") {
				arg = arg[1:]
			}
			var num int
			if _, err := fmt.Sscanf(arg, "%d", &num); err == nil && num > 0 {
				return num, nil
			}
		}
	}

	return 0, errors.New("could not resolve issue/PR number. Please reply to a notification or provide the number as an argument")
}

func (h *CommandHandler) resolveCommentID(ctx *ext.Context) (int64, error) {
	msg := ctx.EffectiveMessage
	if msg.ReplyToMessage != nil {
		key := fmt.Sprintf("%d:%d", ctx.EffectiveChat.Id, msg.ReplyToMessage.MessageId)
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.CommentID != 0 {
			return mContext.CommentID, nil
		}
	}
	return 0, errors.New("could not resolve comment context. Please reply to a comment notification")
}

func (h *CommandHandler) Me(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	user, _, err := client.Users.Get(context.Background(), "")
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch user details: %v", err), nil)
		return nil
	}

	name := "N/A"
	if user.GetName() != "" {
		name = user.GetName()
	}

	email := "N/A"
	if user.GetEmail() != "" {
		email = user.GetEmail()
	}

	msg := fmt.Sprintf(
		"<b>GitHub Profile:</b>\n\n"+
			"• <b>Username:</b> %s\n"+
			"• <b>Name:</b> %s\n"+
			"• <b>Email:</b> %s\n"+
			"• <b>Followers:</b> %d\n"+
			"• <b>Following:</b> %d\n"+
			"• <b>Public Repositories:</b> %d\n"+
			"• <b>Account URL:</b> %s",
		html.EscapeString(user.GetLogin()),
		html.EscapeString(name),
		html.EscapeString(email),
		user.GetFollowers(),
		user.GetFollowing(),
		user.GetPublicRepos(),
		html.EscapeString(user.GetHTMLURL()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Repo(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch repository info: %v", err), nil)
		return nil
	}

	prs, resp, err := client.PullRequests.List(context.Background(), owner, repo, &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 1},
	})
	var openPRs int
	if err == nil {
		if resp.LastPage > 0 {
			openPRs = resp.LastPage
		} else {
			openPRs = len(prs)
		}
	}

	openIssues := max(repoInfo.GetOpenIssuesCount()-openPRs, 0)

	desc := "No description provided."
	if repoInfo.GetDescription() != "" {
		desc = repoInfo.GetDescription()
	}

	visibility := "public"
	if repoInfo.GetPrivate() {
		visibility = "private"
	}

	license := "N/A"
	if repoInfo.GetLicense() != nil && repoInfo.GetLicense().GetName() != "" {
		license = repoInfo.GetLicense().GetName()
	}

	language := "N/A"
	if repoInfo.GetLanguage() != "" {
		language = repoInfo.GetLanguage()
	}

	msg := fmt.Sprintf(
		"<b>Repository:</b> %s\n\n"+
			"• <b>Description:</b> %s\n"+
			"• <b>Visibility:</b> %s\n"+
			"• <b>Default Branch:</b> %s\n"+
			"• <b>Stars:</b> %d\n"+
			"• <b>Forks:</b> %d\n"+
			"• <b>Watchers:</b> %d\n"+
			"• <b>Open Issues:</b> %d\n"+
			"• <b>Open PRs:</b> %d\n"+
			"• <b>License:</b> %s\n"+
			"• <b>Language:</b> %s\n"+
			"• <b>URL:</b> %s",
		html.EscapeString(repoInfo.GetFullName()),
		html.EscapeString(desc),
		html.EscapeString(visibility),
		html.EscapeString(repoInfo.GetDefaultBranch()),
		repoInfo.GetStargazersCount(),
		repoInfo.GetForksCount(),
		repoInfo.GetSubscribersCount(),
		openIssues,
		openPRs,
		html.EscapeString(license),
		html.EscapeString(language),
		html.EscapeString(repoInfo.GetHTMLURL()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Star(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Activity.Star(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to star repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("⭐ Starred repository <b>%s/%s</b>!", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Unstar(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Activity.Unstar(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unstar repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Removed star from <b>%s/%s</b>.", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Watch(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	sub := &github.Subscription{Subscribed: new(true)}
	_, _, err = client.Activity.SetRepositorySubscription(context.Background(), owner, repo, sub)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to watch repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("👁️ Watching repository <b>%s/%s</b>!", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Unwatch(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Activity.DeleteRepositorySubscription(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unwatch repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Stopped watching <b>%s/%s</b>.", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Fork(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	forked, _, err := client.Repositories.CreateFork(context.Background(), owner, repo, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fork repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🍴 Repository forked successfully to <b>%s</b>!", forked.GetFullName()), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Archive(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	update := &github.Repository{Archived: github.Ptr(true)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to archive repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🔒 Repository <b>%s/%s</b> archived.", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Unarchive(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	update := &github.Repository{Archived: github.Ptr(false)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unarchive repository: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🔓 Repository <b>%s/%s</b> unarchived.", owner, repo), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Contributors(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	contribs, _, err := client.Repositories.ListContributors(context.Background(), owner, repo, &github.ListContributorsOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list contributors: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Top Contributors for %s/%s:</b>\n\n", owner, repo))
	for i, c := range contribs {
		msg.WriteString(fmt.Sprintf("%d. <b>%s</b> (%d commits)\n", i+1, html.EscapeString(c.GetLogin()), c.GetContributions()))
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Languages(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	langs, _, err := client.Repositories.ListLanguages(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch languages: %v", err), nil)
		return nil
	}

	var total int64
	for _, bytes := range langs {
		total += int64(bytes)
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Language Statistics for %s/%s:</b>\n\n", owner, repo))
	if total == 0 {
		msg.WriteString("No languages detected.")
	} else {
		for lang, bytes := range langs {
			pct := (float64(bytes) / float64(total)) * 100.0
			msg.WriteString(fmt.Sprintf("• <b>%s</b>: %.2f%%\n", html.EscapeString(lang), pct))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Branches(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	branches, _, err := client.Repositories.ListBranches(context.Background(), owner, repo, &github.BranchListOptions{
		ListOptions: github.ListOptions{PerPage: 20},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list branches: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Branches for %s/%s:</b>\n\n", owner, repo))
	for _, br := range branches {
		msg.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(br.GetName())))
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Branch(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /branch branch-name", nil)
		return nil
	}
	branchName := args[1]

	branch, _, err := client.Repositories.GetBranch(context.Background(), owner, repo, branchName, 3)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Branch not found or failed to fetch: %v", err), nil)
		return nil
	}

	sha := branch.GetCommit().GetSHA()
	protected := "No"
	if branch.GetProtected() {
		protected = "Yes"
	}

	msg := fmt.Sprintf(
		"<b>Branch Details (%s/%s):</b>\n\n"+
			"• <b>Name:</b> %s\n"+
			"• <b>Protected:</b> %s\n"+
			"• <b>Last Commit SHA:</b> %s\n"+
			"• <b>Last Commit Author:</b> %s",
		owner, repo,
		html.EscapeString(branch.GetName()),
		protected,
		html.EscapeString(sha),
		html.EscapeString(branch.GetCommit().GetCommit().GetAuthor().GetName()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Default(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /default branch-name", nil)
		return nil
	}
	branchName := args[1]

	update := &github.Repository{DefaultBranch: github.Ptr(branchName)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to change default branch: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Default branch for <b>%s/%s</b> changed to <b>%s</b>.", owner, repo, html.EscapeString(branchName)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Issue(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	text := ctx.EffectiveMessage.Text
	parts := strings.SplitN(text, "\n", 2)
	titlePart := ""
	bodyPart := ""

	cmdArg := strings.TrimSpace(strings.TrimPrefix(parts[0], "/issue"))
	if strings.HasPrefix(cmdArg, "@") {
		spaceIdx := strings.Index(cmdArg, " ")
		if spaceIdx != -1 {
			cmdArg = strings.TrimSpace(cmdArg[spaceIdx:])
		} else {
			cmdArg = ""
		}
	}

	if cmdArg != "" {
		titlePart = cmdArg
	}

	if len(parts) > 1 {
		bodyPart = strings.TrimSpace(parts[1])
	}

	if titlePart == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /issue Title\n\n[Optional Body]", nil)
		return nil
	}

	req := github.CreateIssueRequest{
		Title: titlePart,
	}
	if bodyPart != "" {
		req.Body = &bodyPart
	}

	issue, _, err := client.Issues.Create(context.Background(), owner, repo, req)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to create issue: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Created issue <b>#%d</b>: <a href=\"%s\">%s</a>", issue.GetNumber(), issue.GetHTMLURL(), html.EscapeString(issue.GetTitle())), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Comment(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	cmdText := ctx.EffectiveMessage.Text
	body := strings.TrimSpace(strings.TrimPrefix(cmdText, "/comment"))
	if strings.HasPrefix(body, "@") {
		spaceIdx := strings.Index(body, " ")
		if spaceIdx != -1 {
			body = strings.TrimSpace(body[spaceIdx:])
		} else {
			body = ""
		}
	}

	if body == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /comment comment-text (replying to a notification)", nil)
		return nil
	}

	comment, _, err := client.Issues.CreateComment(context.Background(), owner, repo, num, &github.IssueComment{Body: &body})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to add comment: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Comment added to <b>#%d</b>: <a href=\"%s\">View</a>", num, comment.GetHTMLURL()), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Assign(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /assign @username", nil)
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.Issues.AddAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to assign user: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Assigned <b>@%s</b> to #%d.", html.EscapeString(target), num), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) AssignMe(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	ghUser, _, err := client.Users.Get(context.Background(), "")
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch authenticated user details: %v", err), nil)
		return nil
	}

	target := ghUser.GetLogin()
	_, _, err = client.Issues.AddAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to assign you: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Assigned you (<b>@%s</b>) to #%d.", html.EscapeString(target), num), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Unassign(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /unassign @username", nil)
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.Issues.RemoveAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unassign user: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Unassigned <b>@%s</b> from #%d.", html.EscapeString(target), num), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Label(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /label +bug -help-wanted", nil)
		return nil
	}

	var toAdd []string
	var toRemove []string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "+") {
			toAdd = append(toAdd, arg[1:])
		} else if strings.HasPrefix(arg, "-") {
			toRemove = append(toRemove, arg[1:])
		} else {
			toAdd = append(toAdd, arg)
		}
	}

	if len(toAdd) > 0 {
		_, _, err = client.Issues.AddLabelsToIssue(context.Background(), owner, repo, num, toAdd)
		if err != nil {
			if h.handleAuthError(b, ctx, err) {
				return nil
			}
			_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to add labels: %v", err), nil)
			return nil
		}
	}

	if len(toRemove) > 0 {
		for _, l := range toRemove {
			_, err = client.Issues.RemoveLabelForIssue(context.Background(), owner, repo, num, l)
			if err != nil {
				// Don't error out entirely
			}
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Labels updated for #%d.", num), nil)
	return err
}

func (h *CommandHandler) Labels(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	var labels []*github.Label
	if err == nil {
		labels, _, err = client.Issues.ListLabelsByIssue(context.Background(), owner, repo, num, nil)
	} else {
		labels, _, err = client.Issues.ListLabels(context.Background(), owner, repo, nil)
	}

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list labels: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	if num > 0 {
		msg.WriteString(fmt.Sprintf("<b>Labels for #%d:</b>\n\n", num))
	} else {
		msg.WriteString(fmt.Sprintf("<b>Labels for %s/%s:</b>\n\n", owner, repo))
	}

	if len(labels) == 0 {
		msg.WriteString("No labels found.")
	} else {
		for _, l := range labels {
			msg.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(l.GetName())))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Milestone(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /milestone v1.0", nil)
		return nil
	}
	mName := args[1]

	milestones, _, err := client.Issues.ListMilestones(context.Background(), owner, repo, &github.MilestoneListOptions{State: "open"})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch milestones: %v", err), nil)
		return nil
	}

	var mNum *int
	for _, m := range milestones {
		if strings.EqualFold(m.GetTitle(), mName) {
			mNum = github.Ptr(m.GetNumber())
			break
		}
	}

	if mNum == nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Milestone '%s' not found.", mName), nil)
		return nil
	}

	req := github.UpdateIssueRequest{Milestone: mNum}
	_, _, err = client.Issues.Update(context.Background(), owner, repo, num, req)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to assign milestone: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Assigned milestone <b>%s</b> to #%d.", html.EscapeString(mName), num), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Lock(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Issues.Lock(context.Background(), owner, repo, num, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to lock: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🔒 Locked conversation on #%d.", num), nil)
	return err
}

func (h *CommandHandler) Unlock(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Issues.Unlock(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unlock: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🔓 Unlocked conversation on #%d.", num), nil)
	return err
}

func (h *CommandHandler) Pin(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	req, err := client.NewRequest(context.Background(), "PUT", fmt.Sprintf("repos/%s/%s/issues/%d/pin", owner, repo, num), nil)
	if err == nil {
		_, err = client.Do(req, nil)
	}

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to pin issue: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("📌 Pinned issue #%d.", num), nil)
	return err
}

func (h *CommandHandler) Unpin(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	req, err := client.NewRequest(context.Background(), "DELETE", fmt.Sprintf("repos/%s/%s/issues/%d/pin", owner, repo, num), nil)
	if err == nil {
		_, err = client.Do(req, nil)
	}

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to unpin issue: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("📌 Unpinned issue #%d.", num), nil)
	return err
}

func (h *CommandHandler) executeGraphQL(ctx context.Context, client *github.Client, query string, variables map[string]any) error {
	body := map[string]any{
		"query":     query,
		"variables": variables,
	}
	req, err := client.NewRequest(ctx, "POST", "graphql", body)
	if err != nil {
		return err
	}
	_, err = client.Do(req, nil)
	return err
}

func (h *CommandHandler) RequestChanges(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	cmdText := ctx.EffectiveMessage.Text
	body := strings.TrimSpace(strings.TrimPrefix(cmdText, "/requestchanges"))
	if strings.HasPrefix(body, "@") {
		spaceIdx := strings.Index(body, " ")
		if spaceIdx != -1 {
			body = strings.TrimSpace(body[spaceIdx:])
		} else {
			body = ""
		}
	}

	if body == "" {
		body = "Changes requested via Telegram."
	}

	review := &github.PullRequestReviewRequest{
		Event: github.Ptr("REQUEST_CHANGES"),
		Body:  github.Ptr(body),
	}
	_, _, err = client.PullRequests.CreateReview(context.Background(), owner, repo, num, review)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to request changes: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✏️ Requested changes on PR #%d.", num), nil)
	return err
}

func (h *CommandHandler) Merge(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	method := "merge"
	if len(args) > 1 {
		methodArg := strings.ToLower(args[1])
		if methodArg == "squash" || methodArg == "rebase" || methodArg == "merge" {
			method = methodArg
		}
	}

	_, _, err = client.PullRequests.Merge(context.Background(), owner, repo, num, "", &github.PullRequestOptions{MergeMethod: method})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to merge PR: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Merged PR #%d using strategy: <b>%s</b>.", num, method), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Draft(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch PR: %v", err), nil)
		return nil
	}

	query := `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	variables := map[string]any{"id": pr.GetNodeID()}

	err = h.executeGraphQL(context.Background(), client, query, variables)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to convert PR to draft: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ PR #%d converted to Draft.", num), nil)
	return err
}

func (h *CommandHandler) Ready(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch PR: %v", err), nil)
		return nil
	}

	query := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	variables := map[string]any{"id": pr.GetNodeID()}

	err = h.executeGraphQL(context.Background(), client, query, variables)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to mark PR as ready: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ PR #%d marked as Ready for Review.", num), nil)
	return err
}

func (h *CommandHandler) Checks(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	var sha string
	num, err := h.resolveIssueOrPRNumber(ctx)
	if err == nil {
		pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
		if err == nil {
			sha = pr.GetHead().GetSHA()
		}
	}

	if sha == "" {
		repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
		if err == nil {
			branch, _, err := client.Repositories.GetBranch(context.Background(), owner, repo, repoInfo.GetDefaultBranch(), 3)
			if err == nil {
				sha = branch.GetCommit().GetSHA()
			}
		}
	}

	if sha == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Could not resolve head commit SHA.", nil)
		return nil
	}

	checks, _, err := client.Checks.ListCheckRunsForRef(context.Background(), owner, repo, sha, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list checks: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Checks status for commit %s:</b>\n\n", html.EscapeString(sha[:7])))
	if checks.GetTotal() == 0 {
		msg.WriteString("No check runs found.")
	} else {
		for _, run := range checks.CheckRuns {
			status := run.GetStatus()
			conclusion := run.GetConclusion()
			emoji := "⏳"
			if status == "completed" {
				if conclusion == "success" {
					emoji = "✓"
				} else if conclusion == "failure" {
					emoji = "✗"
				} else {
					emoji = "❔"
				}
			}
			msg.WriteString(fmt.Sprintf("%-12s %s\n", html.EscapeString(run.GetName()), emoji))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Files(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	files, _, err := client.PullRequests.ListFiles(context.Background(), owner, repo, num, &github.ListOptions{PerPage: 20})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list changed files: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Changed files in PR #%d:</b>\n\n", num))
	for _, f := range files {
		msg.WriteString(fmt.Sprintf("• %s (<b>+%d</b> / <b>-%d</b>)\n", html.EscapeString(f.GetFilename()), f.GetAdditions(), f.GetDeletions()))
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Diff(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch PR: %v", err), nil)
		return nil
	}

	msg := fmt.Sprintf(
		"<b>PR #%d Change Summary:</b>\n\n"+
			"<b>%d</b> files changed\n"+
			"<b>+%d</b> additions\n"+
			"<b>-%d</b> deletions",
		num, pr.GetChangedFiles(), pr.GetAdditions(), pr.GetDeletions(),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Reviews(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	reviews, _, err := client.PullRequests.ListReviews(context.Background(), owner, repo, num, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list reviews: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Review status for PR #%d:</b>\n\n", num))
	if len(reviews) == 0 {
		msg.WriteString("No reviews submitted yet.")
	} else {
		for _, r := range reviews {
			msg.WriteString(fmt.Sprintf("• <b>%s</b>: %s (%s)\n", html.EscapeString(r.GetUser().GetLogin()), html.EscapeString(r.GetState()), html.EscapeString(r.GetSubmittedAt().Format("2006-01-02 15:04"))))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Mergeable(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch PR details: %v", err), nil)
		return nil
	}

	status := "No conflicts"
	if pr.Mergeable != nil && !*pr.Mergeable {
		status = "Has conflicts ⚠️"
	} else if pr.Mergeable == nil {
		status = "Unknown / Checking..."
	}

	msg := fmt.Sprintf(
		"<b>Merge Status for PR #%d:</b>\n\n"+
			"• <b>Mergeable:</b> %s\n"+
			"• <b>Mergeable State:</b> %s",
		num, status, html.EscapeString(pr.GetMergeableState()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) RequestReview(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /request @username", nil)
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.PullRequests.RequestReviewers(context.Background(), owner, repo, num, github.ReviewersRequest{
		Reviewers: []string{target},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to request reviewer: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Review requested from <b>@%s</b> for PR #%d.", html.EscapeString(target), num), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Commit(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /commit SHA", nil)
		return nil
	}
	sha := args[1]

	commit, _, err := client.Repositories.GetCommit(context.Background(), owner, repo, sha, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch commit: %v", err), nil)
		return nil
	}

	msg := fmt.Sprintf(
		"<b>Commit Details (%s/%s):</b>\n\n"+
			"• <b>SHA:</b> %s\n"+
			"• <b>Author:</b> %s\n"+
			"• <b>Message:</b> %s\n"+
			"• <b>HTML URL:</b> %s",
		owner, repo,
		html.EscapeString(commit.GetSHA()),
		html.EscapeString(commit.GetCommit().GetAuthor().GetName()),
		html.EscapeString(commit.GetCommit().GetMessage()),
		html.EscapeString(commit.GetHTMLURL()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Commits(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	commits, _, err := client.Repositories.ListCommits(context.Background(), owner, repo, &github.CommitsListOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list commits: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Commits in %s/%s:</b>\n\n", owner, repo))
	for _, c := range commits {
		shortSHA := c.GetSHA()
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		msg.WriteString(fmt.Sprintf("• <code>%s</code> - %s by <b>%s</b>\n", html.EscapeString(shortSHA), html.EscapeString(strings.Split(c.GetCommit().GetMessage(), "\n")[0]), html.EscapeString(c.GetCommit().GetAuthor().GetName())))
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Compare(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 3 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /compare branch1 branch2", nil)
		return nil
	}
	base := args[1]
	head := args[2]

	comp, _, err := client.Repositories.CompareCommits(context.Background(), owner, repo, base, head, nil)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to compare: %v", err), nil)
		return nil
	}

	msg := fmt.Sprintf(
		"<b>Comparison: %s...%s (%s/%s):</b>\n\n"+
			"• <b>Status:</b> %s\n"+
			"• <b>Ahead By:</b> %d\n"+
			"• <b>Behind By:</b> %d\n"+
			"• <b>Total Commits:</b> %d\n"+
			"• <b>Comparison URL:</b> %s",
		html.EscapeString(base), html.EscapeString(head),
		owner, repo,
		html.EscapeString(comp.GetStatus()),
		comp.GetAheadBy(),
		comp.GetBehindBy(),
		comp.GetTotalCommits(),
		html.EscapeString(comp.GetHTMLURL()),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Actions(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	runs, _, err := client.Actions.ListRepositoryWorkflowRuns(context.Background(), owner, repo, &github.ListWorkflowRunsOptions{
		ListOptions: github.ListOptions{PerPage: 10},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list workflow runs: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Workflow Runs for %s/%s:</b>\n\n", owner, repo))
	if runs.GetTotalCount() == 0 {
		msg.WriteString("No workflow runs found.")
	} else {
		for _, run := range runs.WorkflowRuns {
			status := run.GetStatus()
			conclusion := run.GetConclusion()
			emoji := "⏳"
			if status == "completed" {
				if conclusion == "success" {
					emoji = "✅"
				} else if conclusion == "failure" {
					emoji = "❌"
				} else {
					emoji = "🏁"
				}
			}
			msg.WriteString(fmt.Sprintf("• %s <b>%s</b> #%d: %s (%s)\n", emoji, html.EscapeString(run.GetName()), run.GetRunNumber(), html.EscapeString(status), html.EscapeString(conclusion)))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) RunWorkflow(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /run workflow.yml [branch]", nil)
		return nil
	}
	workflowFile := args[1]

	ref := ""
	if len(args) > 2 {
		ref = args[2]
	} else {
		repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
		if err == nil {
			ref = repoInfo.GetDefaultBranch()
		}
	}

	if ref == "" {
		ref = "main"
	}

	req := github.CreateWorkflowDispatchEventRequest{
		Ref: ref,
	}

	_, _, err = client.Actions.CreateWorkflowDispatchEventByFileName(context.Background(), owner, repo, workflowFile, req)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to trigger workflow: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🚀 Triggered workflow <b>%s</b> on branch <b>%s</b>.", html.EscapeString(workflowFile), html.EscapeString(ref)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) RerunWorkflow(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Actions.RerunWorkflowByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to rerun workflow: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🔄 Workflow run #%d rerunning.", num), nil)
	return err
}

func (h *CommandHandler) CancelWorkflow(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	_, err = client.Actions.CancelWorkflowRunByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to cancel workflow: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("⛔ Workflow run #%d cancelled.", num), nil)
	return err
}

func (h *CommandHandler) WorkflowLogs(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	run, _, err := client.Actions.GetWorkflowRunByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to get workflow run: %v", err), nil)
		return nil
	}

	msg := fmt.Sprintf("📖 <a href=\"%s\">Open workflow logs for run #%d</a>", html.EscapeString(run.GetHTMLURL()+"/jobs"), num)
	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Release(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) > 1 && strings.ToLower(args[1]) == "create" {
		if len(args) < 3 {
			_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /release create v1.0.0", nil)
			return nil
		}
		tagName := args[2]

		req := github.CreateReleaseRequest{
			TagName:              tagName,
			Name:                 github.Ptr(tagName),
			GenerateReleaseNotes: github.Ptr(true),
		}

		rel, _, err := client.Repositories.CreateRelease(context.Background(), owner, repo, req)
		if err != nil {
			if h.handleAuthError(b, ctx, err) {
				return nil
			}
			_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to create release: %v", err), nil)
			return nil
		}

		_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("🚀 Created release <b>%s</b>: <a href=\"%s\">View Release</a>", html.EscapeString(rel.GetTagName()), html.EscapeString(rel.GetHTMLURL())), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
		return err
	}

	rel, _, err := client.Repositories.GetLatestRelease(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch latest release or none found: %v", err), nil)
		return nil
	}

	name := rel.GetTagName()
	if rel.GetName() != "" {
		name = rel.GetName()
	}

	msg := fmt.Sprintf(
		"<b>Latest Release (%s/%s):</b>\n\n"+
			"• <b>Name:</b> %s\n"+
			"• <b>Tag:</b> %s\n"+
			"• <b>Published:</b> %s\n"+
			"• <b>URL:</b> %s",
		owner, repo,
		html.EscapeString(name),
		html.EscapeString(rel.GetTagName()),
		html.EscapeString(rel.GetPublishedAt().Format("2006-01-02 15:04")),
		html.EscapeString(rel.GetHTMLURL()),
	)

	if rel.GetBody() != "" {
		body := rel.GetBody()
		if len(body) > 300 {
			body = body[:300] + "..."
		}
		msg += fmt.Sprintf("\n\n<b>Notes:</b>\n<i>%s</i>", html.EscapeString(body))
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Changelog(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	tagName := "vNext"
	args := ctx.Args()
	if len(args) > 1 {
		tagName = args[1]
	}

	notesReq := github.GenerateNotesRequest{
		TagName: tagName,
	}

	notes, _, err := client.Repositories.GenerateReleaseNotes(context.Background(), owner, repo, notesReq)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to generate changelog: %v", err), nil)
		return nil
	}

	msg := fmt.Sprintf("<b>Generated Changelog (%s/%s):</b>\n\n%s", owner, repo, notes.GetBody())
	if len(msg) > 4000 {
		msg = msg[:4000] + "..."
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) CreateDiscussion(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	text := ctx.EffectiveMessage.Text
	parts := strings.SplitN(text, "\n", 2)
	titlePart := ""
	bodyPart := ""

	cmdArg := strings.TrimSpace(strings.TrimPrefix(parts[0], "/discussion"))
	if strings.HasPrefix(cmdArg, "@") {
		spaceIdx := strings.Index(cmdArg, " ")
		if spaceIdx != -1 {
			cmdArg = strings.TrimSpace(cmdArg[spaceIdx:])
		} else {
			cmdArg = ""
		}
	}

	if cmdArg != "" {
		titlePart = cmdArg
	}

	if len(parts) > 1 {
		bodyPart = strings.TrimSpace(parts[1])
	}

	if titlePart == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /discussion Title\n\n[Optional Body]", nil)
		return nil
	}

	queryRepo := `query($owner: String!, $name: String!) {
		repository(owner: $owner, name: $name) {
			id
			discussionCategories(first: 10) {
				nodes {
					id
					name
				}
			}
		}
	}`

	variables := map[string]any{"owner": owner, "name": repo}
	body := map[string]any{
		"query":     queryRepo,
		"variables": variables,
	}

	req, err := client.NewRequest(context.Background(), "POST", "graphql", body)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to query repository details: %v", err), nil)
		return nil
	}

	var respData struct {
		Data struct {
			Repository struct {
				ID                   string `json:"id"`
				DiscussionCategories struct {
					Nodes []struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"discussionCategories"`
			} `json:"repository"`
		} `json:"data"`
	}

	_, err = client.Do(req, &respData)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to execute GraphQL: %v", err), nil)
		return nil
	}

	repoID := respData.Data.Repository.ID
	if repoID == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to resolve repository ID or discussions might not be enabled.", nil)
		return nil
	}

	categories := respData.Data.Repository.DiscussionCategories.Nodes
	if len(categories) == 0 {
		_, _ = ctx.EffectiveMessage.Reply(b, "No discussion categories found. Ensure Discussions are enabled on the repository settings.", nil)
		return nil
	}

	categoryID := categories[0].ID
	for _, cat := range categories {
		if strings.EqualFold(cat.Name, "General") || strings.EqualFold(cat.Name, "Q&A") {
			categoryID = cat.ID
			break
		}
	}

	mutationCreate := `mutation($repositoryId: ID!, $categoryId: ID!, $title: String!, $body: String!) {
		createDiscussion(input: {repositoryId: $repositoryId, categoryId: $categoryId, title: $title, body: $body}) {
			discussion {
				number
				url
			}
		}
	}`

	bodyCreate := map[string]any{
		"query": mutationCreate,
		"variables": map[string]any{
			"repositoryId": repoID,
			"categoryId":   categoryID,
			"title":        titlePart,
			"body":         bodyPart,
		},
	}

	reqCreate, err := client.NewRequest(context.Background(), "POST", "graphql", bodyCreate)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to build create discussion request.", nil)
		return nil
	}

	var respCreate struct {
		Data struct {
			CreateDiscussion struct {
				Discussion struct {
					Number int    `json:"number"`
					URL    string `json:"url"`
				} `json:"discussion"`
			} `json:"createDiscussion"`
		} `json:"data"`
	}

	_, err = client.Do(reqCreate, &respCreate)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to create discussion: %v", err), nil)
		return nil
	}

	disc := respCreate.Data.CreateDiscussion.Discussion
	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("✅ Created discussion <b>#%d</b>: <a href=\"%s\">View Discussion</a>", disc.Number, html.EscapeString(disc.URL)), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Answered(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	commentID, err := h.resolveCommentID(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	queryComment := `query($owner: String!, $name: String!, $dbId: Int!) {
		repository(owner: $owner, name: $name) {
			discussions(first: 10) {
				nodes {
					comments(first: 100) {
						nodes {
							id
							databaseId
							replies(first: 100) {
								nodes {
									id
									databaseId
								}
							}
						}
					}
				}
			}
		}
	}`

	body := map[string]any{
		"query": queryComment,
		"variables": map[string]any{
			"owner": owner,
			"name":  repo,
			"dbId":  commentID,
		},
	}

	req, err := client.NewRequest(context.Background(), "POST", "graphql", body)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to query comment details.", nil)
		return nil
	}

	var respData struct {
		Data struct {
			Repository struct {
				Discussions struct {
					Nodes []struct {
						Comments struct {
							Nodes []struct {
								ID         string `json:"id"`
								DatabaseID int64  `json:"databaseId"`
								Replies    struct {
									Nodes []struct {
										ID         string `json:"id"`
										DatabaseID int64  `json:"databaseId"`
									} `json:"nodes"`
								} `json:"replies"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"discussions"`
			} `json:"repository"`
		} `json:"data"`
	}

	_, err = client.Do(req, &respData)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch comments: %v", err), nil)
		return nil
	}

	var commentNodeID string
	for _, disc := range respData.Data.Repository.Discussions.Nodes {
		for _, comment := range disc.Comments.Nodes {
			if comment.DatabaseID == commentID {
				commentNodeID = comment.ID
				break
			}
			for _, reply := range comment.Replies.Nodes {
				if reply.DatabaseID == commentID {
					commentNodeID = reply.ID
					break
				}
			}
		}
		if commentNodeID != "" {
			break
		}
	}

	if commentNodeID == "" {
		_, _ = ctx.EffectiveMessage.Reply(b, "Could not find comment node ID on GitHub.", nil)
		return nil
	}

	mutationAnswer := `mutation($id: ID!) {
		markDiscussionCommentAsAnswer(input: {id: $id}) {
			discussion {
				url
			}
		}
	}`

	bodyAnswer := map[string]any{
		"query":     mutationAnswer,
		"variables": map[string]any{"id": commentNodeID},
	}

	reqAnswer, err := client.NewRequest(context.Background(), "POST", "graphql", bodyAnswer)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to build answer mutation.", nil)
		return nil
	}

	_, err = client.Do(reqAnswer, nil)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to mark comment as answer: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, "✅ Discussion marked as answered!", nil)
	return err
}

func (h *CommandHandler) Find(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /find keyword", nil)
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s is:issue", keyword, owner, repo)
	result, _, err := client.Search.Issues(context.Background(), query, &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 5},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Search failed: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Search Issues results for '%s':</b>\n\n", html.EscapeString(keyword)))
	if result.GetTotal() == 0 {
		msg.WriteString("No issues found.")
	} else {
		for _, issue := range result.Issues {
			msg.WriteString(fmt.Sprintf("• <a href=\"%s\">#%d %s</a>\n", html.EscapeString(issue.GetHTMLURL()), issue.GetNumber(), html.EscapeString(issue.GetTitle())))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) PRSearch(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /pr keyword", nil)
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s is:pr", keyword, owner, repo)
	result, _, err := client.Search.Issues(context.Background(), query, &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 5},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("PR search failed: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Search Pull Requests results for '%s':</b>\n\n", html.EscapeString(keyword)))
	if result.GetTotal() == 0 {
		msg.WriteString("No PRs found.")
	} else {
		for _, pr := range result.Issues {
			msg.WriteString(fmt.Sprintf("• <a href=\"%s\">#%d %s</a>\n", html.EscapeString(pr.GetHTMLURL()), pr.GetNumber(), html.EscapeString(pr.GetTitle())))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) SearchCode(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	args := ctx.Args()
	if len(args) < 2 {
		_, _ = ctx.EffectiveMessage.Reply(b, "Usage: /search keyword", nil)
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s", keyword, owner, repo)
	result, _, err := client.Search.Code(context.Background(), query, &github.SearchOptions{
		ListOptions: github.ListOptions{PerPage: 5},
	})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Code search failed: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Search Code results for '%s':</b>\n\n", html.EscapeString(keyword)))
	if result.GetTotal() == 0 {
		msg.WriteString("No code results found.")
	} else {
		for _, res := range result.CodeResults {
			msg.WriteString(fmt.Sprintf("• <a href=\"%s\">%s</a> in <code>%s</code>\n", html.EscapeString(res.GetHTMLURL()), html.EscapeString(res.GetName()), html.EscapeString(res.GetPath())))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) findNotificationThreadID(ctx context.Context, client *github.Client, owner, repo string, issueNum int) (string, error) {
	notifs, _, err := client.Activity.ListRepositoryNotifications(ctx, owner, repo, &github.NotificationListOptions{All: true})
	if err != nil {
		return "", err
	}
	for _, n := range notifs {
		suffixIssue := fmt.Sprintf("/issues/%d", issueNum)
		suffixPR := fmt.Sprintf("/pulls/%d", issueNum)
		if strings.HasSuffix(n.GetSubject().GetURL(), suffixIssue) || strings.HasSuffix(n.GetSubject().GetURL(), suffixPR) {
			return n.GetID(), nil
		}
	}
	return "", errors.New("notification not found")
}

func (h *CommandHandler) Mute(b *gotgbot.Bot, ctx *ext.Context) error {
	threadID := ctx.EffectiveMessage.MessageThreadId
	err := h.DB.MuteThread(context.Background(), ctx.EffectiveChat.Id, threadID)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Failed to mute this thread.", nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, "🔕 This thread has been muted. You will no longer receive notifications here.", nil)
	return err
}

func (h *CommandHandler) Done(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	threadID, err := h.findNotificationThreadID(context.Background(), client, owner, repo, num)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Could not find a corresponding GitHub notification for this issue/PR.", nil)
		return nil
	}

	_, err = client.Activity.MarkThreadDone(context.Background(), threadID)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to mark as done: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, "✅ Marked notification as done.", nil)
	return err
}

func (h *CommandHandler) Read(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	threadID, err := h.findNotificationThreadID(context.Background(), client, owner, repo, num)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Could not find a corresponding GitHub notification for this issue/PR.", nil)
		return nil
	}

	_, err = client.Activity.MarkThreadRead(context.Background(), threadID)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to mark as read: %v", err), nil)
		return nil
	}

	_, err = ctx.EffectiveMessage.Reply(b, "✅ Marked notification as read.", nil)
	return err
}

func (h *CommandHandler) Stats(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to fetch statistics: %v", err), nil)
		return nil
	}

	prs, prResp, err := client.PullRequests.List(context.Background(), owner, repo, &github.PullRequestListOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 1},
	})
	var openPRs int
	if err == nil {
		if prResp.LastPage > 0 {
			openPRs = prResp.LastPage
		} else {
			openPRs = len(prs)
		}
	}

	openIssues := max(repoInfo.GetOpenIssuesCount()-openPRs, 0)

	contribs, _, _ := client.Repositories.ListContributors(context.Background(), owner, repo, &github.ListContributorsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	})
	contribsCount := len(contribs)

	latestRelease := "N/A"
	rel, _, relErr := client.Repositories.GetLatestRelease(context.Background(), owner, repo)
	if relErr == nil {
		latestRelease = rel.GetTagName()
	}

	lastCommit := "N/A"
	commits, _, commitErr := client.Repositories.ListCommits(context.Background(), owner, repo, &github.CommitsListOptions{
		ListOptions: github.ListOptions{PerPage: 1},
	})
	if commitErr == nil && len(commits) > 0 {
		lastCommit = fmt.Sprintf("%s (by %s)", commits[0].GetSHA()[:7], commits[0].GetCommit().GetAuthor().GetName())
	}

	msg := fmt.Sprintf(
		"<b>Repository Statistics for %s/%s:</b>\n\n"+
			"⭐ <b>Stars:</b> %d\n"+
			"🍴 <b>Forks:</b> %d\n"+
			"📌 <b>Open Issues:</b> %d\n"+
			"🚀 <b>Open PRs:</b> %d\n"+
			"👥 <b>Contributors:</b> %d\n"+
			"🏷️ <b>Latest Release:</b> %s\n"+
			"🔨 <b>Last Commit:</b> %s",
		owner, repo,
		repoInfo.GetStargazersCount(),
		repoInfo.GetForksCount(),
		openIssues,
		openPRs,
		contribsCount,
		html.EscapeString(latestRelease),
		html.EscapeString(lastCommit),
	)

	_, err = ctx.EffectiveMessage.Reply(b, msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Activity(b *gotgbot.Bot, ctx *ext.Context) error {
	client, err := h.getAuthenticatedClient(b, ctx)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(ctx)
	if err != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "⚠️ "+err.Error(), nil)
		return nil
	}

	events, _, err := client.Activity.ListRepositoryEvents(context.Background(), owner, repo, &github.ListOptions{PerPage: 10})
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Failed to list activity: %v", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Activity in %s/%s:</b>\n\n", owner, repo))
	if len(events) == 0 {
		msg.WriteString("No recent activity found.")
	} else {
		for _, e := range events {
			evtType := e.GetType()
			evtType = strings.TrimSuffix(evtType, "Event")
			msg.WriteString(fmt.Sprintf("• <b>%s</b> by %s (%s)\n", html.EscapeString(evtType), html.EscapeString(e.GetActor().GetLogin()), e.GetCreatedAt().Format("2006-01-02 15:04")))
		}
	}

	_, err = ctx.EffectiveMessage.Reply(b, msg.String(), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}
