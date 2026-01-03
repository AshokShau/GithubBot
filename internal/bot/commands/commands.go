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

	"html"
	"net/http"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/google/go-github/v80/github"
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

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	var owner, repo string
	if n := len(repoFullName); n > 0 {
		for i := 0; i < n; i++ {
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

	// Verify repository existence
	_, _, getErr := client.Repositories.Get(context.Background(), owner, repo)
	if getErr != nil {
		if h.handleAuthError(b, ctx, getErr) {
			return nil
		}
		var errResp *github.ErrorResponse
		if errors.As(getErr, &errResp) && errResp.Response.StatusCode == http.StatusNotFound {
			_, _ = ctx.EffectiveMessage.Reply(b, "❌ <b>Repository not found.</b>\nPlease check the name and ensure you have access.", &gotgbot.SendMessageOpts{ParseMode: "HTML"})
			return nil
		}
		_, _ = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Error fetching repository: %v", getErr), nil)
		return nil
	}

	token, encErr := utils.Encrypt(fmt.Sprintf("%d", ctx.EffectiveChat.Id), h.EncryptionKey)
	if encErr != nil {
		_, _ = ctx.EffectiveMessage.Reply(b, "Error generating webhook token.", nil)
		return nil
	}

	webhookURL := fmt.Sprintf("%s/webhook/%s", h.Config.TelegramWebhookURL, token)
	webhookConfig := &github.HookConfig{
		URL:         github.String(webhookURL),
		ContentType: github.String("json"),
		Secret:      github.String(h.Config.GitHubWebhookSecret),
	}

	var defaultEvents []string
	for _, e := range gh.SupportedEvents {
		defaultEvents = append(defaultEvents, e.Name)
	}

	hook := &github.Hook{
		Name:   github.String("web"),
		Events: defaultEvents,
		Config: webhookConfig,
		Active: github.Bool(true),
	}

	createdHook, _, hookErr := client.Repositories.CreateHook(context.Background(), owner, repo, hook)
	if hookErr != nil {
		if h.handleAuthError(b, ctx, hookErr) {
			return nil
		}
		var errResp *github.ErrorResponse
		if errors.As(hookErr, &errResp) && errResp.Response.StatusCode == http.StatusNotFound {
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

	err := h.DB.AddRepoLink(context.Background(), ctx.EffectiveChat.Id, link)
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

	client := h.ClientFactory.GetUserClient(context.Background(), token)
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

	startPage := page - 1
	if startPage < 1 {
		startPage = 1
	}
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
	if err == nil && link.WebhookID != 0 {
		user, uErr := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
		if uErr == nil && user.EncryptedOAuthToken != "" {
			token, _ := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			client := h.ClientFactory.GetUserClient(context.Background(), token)

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
					_ = h.handleAuthError(b, ctx, err)
				}
			}
		}
	}

	err = h.DB.RemoveRepoLink(context.Background(), ctx.EffectiveChat.Id, repoFullName)
	if err != nil {
		_, err := ctx.EffectiveMessage.Reply(b, "Error removing repository or not found.", nil)
		return err
	}

	_, err = ctx.EffectiveMessage.Reply(b, fmt.Sprintf("Repository <b>%s</b> removed successfully.", repoFullName), &gotgbot.SendMessageOpts{ParseMode: "HTML"})
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

	var msg string
	for _, l := range links {
		msg += fmt.Sprintf("• <b>%s</b>\n", l.RepoFullName)
	}

	_, err = ctx.EffectiveMessage.Reply(b, "<b>Linked Repositories:</b>\n"+msg, &gotgbot.SendMessageOpts{ParseMode: "HTML"})
	return err
}

func (h *CommandHandler) Help(b *gotgbot.Bot, ctx *ext.Context) error {
	msg := `<b>GitHub Bot Commands:</b>

<b>Account</b>
/connect - Link your GitHub account (<i>Must be used in private chat</i>)

<b>Repository Management</b>
/addrepo [owner/repo] - Link a repository
/removerepo [owner/repo] - Unlink a repository
/repos - List linked repositories
/close - Close an issue or PR (reply to notification).
/reopen - Reopen an issue or PR (reply to notification).
/approve - Approve a PR (reply to notification).

<b>Configuration</b>
/settings - Configure event notifications
/reload - Reload admin cache


<b>Need more help?</b>
Visit the <a href="https://github.com/AshokShau/GithubBot">GitHub page</a> for more details.`

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
	if errResp, ok := err.(*github.ErrorResponse); ok {
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
		Event: github.String("APPROVE"),
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

	req := &github.IssueRequest{State: &state}
	_, _, err = client.Issues.Edit(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, req)

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

	return h.ClientFactory.GetUserClient(context.Background(), token), nil
}
