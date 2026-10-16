package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	gh "github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	"github.com/google/go-github/v92/github"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func getTopicID(m *gotdbot.Message) int32 {
	if m.TopicId != nil {
		if t, ok := m.TopicId.(*gotdbot.MessageTopicForum); ok {
			return t.ForumTopicId
		}
	}
	return 0
}

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

func (h *CommandHandler) Start(c *gotdbot.Client, m *gotdbot.Message) error {
	msg := `<b>GitHub Bot</b>

I can help you manage your GitHub repositories and notifications directly from Telegram.

<b>Get Started:</b>
1. Use <code>/connect</code> to link your GitHub account.
2. Use <code>/addrepo</code> to link a repository and start receiving notifications.
3. Use <code>/settings</code> to customize your notification preferences.

Need help? Type <code>/help</code> for a full list of commands.`
	_, err := m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Connect(c *gotdbot.Client, m *gotdbot.Message) error {
	if !m.IsPrivate() {
		_, err := m.ReplyText(c, "The /connect command can only be used in a private chat with the bot.", nil)
		return err
	}

	state, err := gh.GenerateState()
	if err != nil {
		c.Logger.Warnf("Could not start GitHub authorization: %s", utils.ErrorDetails(err))
		_, replyErr := m.ReplyText(c, utils.OperationError("Could not start GitHub authorization", err), nil)
		return replyErr
	}

	h.StateCache.Set(state, m.SenderID(), 10*time.Minute)

	url := h.OAuth.GetLoginURL(state)

	msg := "<b>Connect GitHub</b>\n\n" + utils.Link("Authorize your GitHub account", url) + "\n\nThis link expires in 10 minutes. After connecting, use /addrepo to choose a repository."
	_, err = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) AddRepo(c *gotdbot.Client, m *gotdbot.Message) error {
	if !m.IsPrivate() && !utils.IsAdmin(c, m.ChatId, m.SenderID(), h.AdminCache) {
		_, err := m.ReplyText(c, "Telegram administrator access is required. Ask an administrator to add the repository.", nil)
		return err
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		return h.listUserRepos(c, m)
	}

	repoFullName := args[1]
	user, uErr := h.DB.GetUserByTelegramID(context.Background(), m.SenderID())
	if uErr != nil || user == nil || user.EncryptedOAuthToken == "" {
		if uErr != nil && !errors.Is(uErr, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(uErr))
		}
		msg := utils.ConnectionError(uErr)
		_, _ = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}

	token, decErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if decErr != nil {
		_, _ = m.ReplyText(c, "GitHub authorization is unavailable. Use /connect to reconnect.", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_, _ = m.ReplyText(c, "GitHub is unavailable. Please try again later.", nil)
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
		_, _ = m.ReplyText(c, "Invalid repository format. Use owner/repo", nil)
		return nil
	}

	if existingLink, _ := h.DB.GetRepoLink(context.Background(), m.ChatId, repoFullName); existingLink != nil {
		msg := fmt.Sprintf("Repository <b>%s</b> is already linked to this chat.", html.EscapeString(repoFullName))
		_, err = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return err
	}

	_, _, getErr := client.Repositories.Get(context.Background(), owner, repo)
	if getErr != nil {
		if h.handleAuthError(c, m, getErr) {
			return nil
		}
		if errResp, ok := errors.AsType[*github.ErrorResponse](getErr); ok && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound {
			_, _ = m.ReplyText(c, "<b>Repository not found.</b>\nPlease check the name and ensure you have access.", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
			return nil
		}
		c.Logger.Warnf("Error fetching repository: %s", utils.ErrorDetails(getErr))
		_, _ = m.ReplyText(c, utils.OperationError("Error fetching repository", getErr), nil)
		return nil
	}

	payload := fmt.Sprintf("%d", m.ChatId)
	if getTopicID(m) != 0 {
		payload = fmt.Sprintf("%d:%d", m.ChatId, getTopicID(m))
	}

	token, encErr := utils.Encrypt(payload, h.EncryptionKey)
	if encErr != nil {
		_, _ = m.ReplyText(c, "Could not prepare the webhook. Try again or contact the bot administrator.", nil)
		return nil
	}

	webhookURL := fmt.Sprintf("%s/webhook/%s", h.Config.TelegramWebhookURL, token)
	webhookConfig := &github.HookConfig{
		URL:         new(webhookURL),
		ContentType: new("json"),
		Secret:      new(h.Config.GitHubWebhookSecret),
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
		if h.handleAuthError(c, m, hookErr) {
			return nil
		}
		if errResp, ok := errors.AsType[*github.ErrorResponse](hookErr); ok && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound {
			safeRepoName := html.EscapeString(repoFullName)
			msg := fmt.Sprintf("<b>Insufficient permissions.</b>\nYou need admin access to repository <b>%s</b> to create webhooks.", safeRepoName)
			_, err := m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
			return err
		}

		c.Logger.Warnf("Create repository webhook: %s", utils.ErrorDetails(hookErr))
		msg := "<b>Webhook creation failed.</b>\nPlease ensure you have admin rights and try again."
		_, err := m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return err
	}

	webhookID := createdHook.GetID()
	link := models.RepoLink{
		RepoFullName: repoFullName,
		WebhookID:    webhookID,
	}

	err = h.DB.AddRepoLink(context.Background(), m.ChatId, link)
	if err != nil {
		c.Logger.Warnf("Save repository link: %s", utils.ErrorDetails(err))
		if _, cleanupErr := client.Repositories.DeleteHook(context.Background(), owner, repo, webhookID); cleanupErr != nil {
			c.Logger.Warnf("Roll back repository webhook: %s", utils.ErrorDetails(cleanupErr))
			_, replyErr := m.ReplyText(c, "The webhook was created, but the link could not be saved. Remove the webhook in GitHub settings before trying /addrepo again.", nil)
			return replyErr
		}
		_, err := m.ReplyText(c, "Could not save the repository link. Try again or contact the bot administrator.", nil)
		return err
	}

	msg := fmt.Sprintf("Repository <b>%s</b> linked successfully!", html.EscapeString(repoFullName))
	_, err = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) listUserRepos(c *gotdbot.Client, m *gotdbot.Message) error {
	return h.sendRepoList(c, m, 1)
}

func (h *CommandHandler) sendRepoList(c *gotdbot.Client, m *gotdbot.Message, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), m.SenderID())
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		_, _ = m.ReplyText(c, utils.ConnectionError(err), nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = m.ReplyText(c, "GitHub authorization is unavailable. Use /connect to reconnect.", nil)
		return nil
	}

	client, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_, _ = m.ReplyText(c, "GitHub is unavailable. Please try again later.", nil)
		return nil
	}

	opts := &github.RepositoryListOptions{
		Sort:      "updated",
		Direction: "desc",
		PerPage:   5, Page: page,
	}

	repos, resp, err := client.Repositories.List(context.Background(), "", opts)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		_, _ = m.ReplyText(c, "Could not fetch repositories from GitHub. Please try again later.", nil)
		return nil
	}

	if len(repos) == 0 && page == 1 {
		_, _ = m.ReplyText(c, "No accessible repositories found. Check the connected GitHub account and its repository permissions.", nil)
		return nil
	}

	var kb [][]gotdbot.InlineKeyboardButton
	for _, repo := range repos {
		kb = append(kb, []gotdbot.InlineKeyboardButton{
			{Text: repo.GetFullName(), Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:id:%d", repo.GetID())}},
		})
	}

	var navRow []gotdbot.InlineKeyboardButton

	if resp.FirstPage != 0 && resp.PrevPage != 0 {
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: "< Prev", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", resp.PrevPage)}})
	}

	startPage := max(page-1, 1)
	endPage := page
	if resp.NextPage != 0 {
		endPage = resp.NextPage
	}
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
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: text, Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", i)}})
	}

	if resp.NextPage != 0 {
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: "Next >", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", resp.NextPage)}})
	}

	if len(navRow) > 0 {
		kb = append(kb, navRow)
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Select a repository to add (Page %d):", page), &gotdbot.SendMessageOpts{
		ReplyMarkup:           &gotdbot.ReplyMarkupInlineKeyboard{Rows: kb},
		DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Settings(c *gotdbot.Client, m *gotdbot.Message) error {
	if !m.IsPrivate() && !utils.IsAdmin(c, m.ChatId, m.SenderID(), h.AdminCache) {
		_, err := m.ReplyText(c, "Telegram administrator access is required. Ask an administrator to change settings.", nil)
		return err
	}

	links, err := h.DB.GetChatLinks(context.Background(), m.ChatId)
	if err != nil {
		c.Logger.Warnf("Could not load linked repositories: %s", utils.ErrorDetails(err))
		_, replyErr := m.ReplyText(c, utils.OperationError("Could not load linked repositories", err), nil)
		return replyErr
	}

	if len(links) == 0 {
		_, err = m.ReplyText(c, "No repositories linked. Use /addrepo to link one.", nil)
		return err
	}

	var kb [][]gotdbot.InlineKeyboardButton
	for _, l := range links {
		kb = append(kb, []gotdbot.InlineKeyboardButton{
			{Text: l.RepoFullName, Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:r:%s", l.RepoFullName)}},
		})
	}

	_, err = m.ReplyText(c, "Select a repository to configure:", &gotdbot.SendMessageOpts{
		ReplyMarkup:           &gotdbot.ReplyMarkupInlineKeyboard{Rows: kb},
		DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) RemoveRepo(c *gotdbot.Client, m *gotdbot.Message) error {
	if !m.IsPrivate() && !utils.IsAdmin(c, m.ChatId, m.SenderID(), h.AdminCache) {
		_, err := m.ReplyText(c, "Telegram administrator access is required. Ask an administrator to remove the repository.", nil)
		return err
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, err := m.ReplyText(c, "<b>Usage</b>\n\n<code>/removerepo owner/repo</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return err
	}

	repoFullName := args[1]
	link, err := h.DB.GetRepoLink(context.Background(), m.ChatId, repoFullName)
	if err != nil {
		_, err := m.ReplyText(c, "Repository link unavailable. Use /repos to check linked repositories and try again.", nil)
		return err
	}

	var webhookStatusMsg string

	if link.WebhookID != 0 {
		user, uErr := h.DB.GetUserByTelegramID(context.Background(), m.SenderID())
		if uErr != nil || user == nil || user.EncryptedOAuthToken == "" {
			webhookStatusMsg = "\n\n <b>Warning:</b> You are not connected to GitHub. The webhook could not be removed from the repository settings. Please remove it manually."
		} else {
			token, decErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			if decErr != nil {
				webhookStatusMsg = "\n\n <b>Warning:</b> GitHub authorization is unavailable. Use /connect, or remove the webhook in the repository settings."
			} else {
				client, err := h.ClientFactory.GetUserClient(context.Background(), token)
				if err != nil {
					webhookStatusMsg = "\n\n <b>Warning:</b> GitHub is unavailable. Please try again later. Webhook not removed."
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
							if h.handleAuthError(c, m, err) {
								webhookStatusMsg = "\n\n <b>Warning:</b> GitHub authentication failed. Webhook not removed."
							} else {
								if errResp, ok := errors.AsType[*github.ErrorResponse](err); ok && errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotFound {
								} else {
									c.Logger.Warnf("\\n\\n <b>Warning:</b> Failed to remove webhook from GitHub: %s", utils.ErrorDetails(err))
									webhookStatusMsg = utils.OperationError("\\n\\n <b>Warning:</b> Failed to remove webhook from GitHub", err)
								}
							}
						}
					}
				}
			}
		}
	}

	err = h.DB.RemoveRepoLink(context.Background(), m.ChatId, repoFullName)
	if err != nil {
		_, err := m.ReplyText(c, "Could not remove the repository link. Please try again.", nil)
		return err
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Repository <b>%s</b> removed successfully.%s", html.EscapeString(repoFullName), webhookStatusMsg), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Repos(c *gotdbot.Client, m *gotdbot.Message) error {
	links, err := h.DB.GetChatLinks(context.Background(), m.ChatId)
	if err != nil {
		c.Logger.Warnf("Could not load linked repositories: %s", utils.ErrorDetails(err))
		_, replyErr := m.ReplyText(c, utils.OperationError("Could not load linked repositories", err), nil)
		return replyErr
	}

	if len(links) == 0 {
		_, err = m.ReplyText(c, "No repositories linked. Use /addrepo to link one.", nil)
		return err
	}

	var msg strings.Builder
	for _, l := range links {
		msg.WriteString(fmt.Sprintf("• <b>%s</b>\n", html.EscapeString(l.RepoFullName)))
	}

	_, err = m.ReplyText(c, utils.LimitHTML("<b>Linked Repositories:</b>\n"+msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Help(c *gotdbot.Client, m *gotdbot.Message) error {
	msg := `<b>GitHub Bot Commands:</b>

<b>Authentication</b>
<code>/connect</code> - Connect your GitHub account using OAuth
<code>/disconnect</code> - Disconnect your GitHub account
<code>/me</code> - Show your connected GitHub account details

<b>Repository Management</b>
<code>/addrepo owner/repo</code> - Link a repository to this chat
<code>/removerepo owner/repo</code> - Remove a linked repository
<code>/repos</code> - List all linked repositories
<code>/repo [owner/repo]</code> - Show repository information
<code>/star</code> - Star the current repository
<code>/unstar</code> - Remove your star
<code>/watch</code> - Watch repository
<code>/unwatch</code> - Stop watching repository
<code>/fork</code> - Fork repository to your account
<code>/archive</code> - Archive repository
<code>/unarchive</code> - Unarchive repository
<code>/contributors</code> - Show top contributors
<code>/languages</code> - Show language statistics
<code>/branches</code> - List branches
<code>/branch branch-name</code> - Show branch details
<code>/default branch-name</code> - Change default branch

<b>Issues</b>
<code>/issue</code> - Create a new issue
<code>/comment</code> - Add a comment to the replied issue or PR
<code>/close</code> - Close issue or PR (reply to notification)
<code>/reopen</code> - Reopen issue or PR (reply to notification)
<code>/assign @username</code> - Assign a user
<code>/assignme</code> - Assign yourself
<code>/unassign @username</code> - Unassign a user
<code>/label bug</code> - Add label
<code>/label +bug -help wanted</code> - Add/remove multiple labels
<code>/labels</code> - List labels
<code>/milestone v1.0</code> - Assign milestone
<code>/lock</code> - Lock conversation
<code>/unlock</code> - Unlock conversation
<code>/pin</code> - Pin issue
<code>/unpin</code> - Unpin issue

<b>Pull Requests</b>
<code>/approve</code> - Approve PR (reply to notification)
<code>/requestchanges</code> - Request changes
<code>/merge</code> - Merge PR using default strategy
<code>/merge squash</code> - Squash merge
<code>/merge rebase</code> - Rebase merge
<code>/merge merge</code> - Merge commit
<code>/draft</code> - Convert PR to Draft
<code>/ready</code> - Mark Draft PR as Ready
<code>/checks</code> - Show CI status
<code>/files</code> - List changed files
<code>/diff</code> - Show change summary
<code>/reviews</code> - Show review status
<code>/mergeable</code> - Check merge conflicts
<code>/request @username</code> - Request reviewer

<b>Commits</b>
<code>/commit SHA</code> - Show commit details
<code>/commits</code> - Show recent commits
<code>/compare branch1 branch2</code> - Compare branches

<b>GitHub Actions</b>
<code>/actions</code> - List recent workflow runs
<code>/run workflow.yml</code> - Run workflow manually
<code>/rerun</code> - Rerun failed workflow (reply to notification)
<code>/cancel</code> - Cancel running workflow (reply to notification)
<code>/logs</code> - Open workflow logs

<b>Releases</b>
<code>/release</code> - Show latest release
<code>/release create v1.0.0</code> - Create release
<code>/changelog</code> - Generate release notes

<b>Discussions</b>
<code>/discussion</code> - Create discussion
<code>/answered</code> - Mark discussion as answered

<b>Search</b>
<code>/find keyword</code> - Search issues
<code>/pr keyword</code> - Search pull requests
<code>/search keyword</code> - Search repository code

<b>Notifications &amp; Stats</b>
<code>/mute</code> - Mute current thread
<code>/done</code> - Mark notification as done (reply to notification)
<code>/read</code> - Mark notification as read (reply to notification)
<code>/stats</code> - Repository statistics
<code>/activity</code> - Recent repository activity

<b>Settings &amp; Help</b>
<code>/settings</code> - Configure event notifications
<code>/reload</code> - Reload admin cache
<code>/privacy</code> - View the privacy policy
<code>/help</code> - Show this help menu`

	// Keep the complete command reference available in two short messages.
	var chunk strings.Builder
	for section := range strings.SplitSeq(msg, "\n\n") {
		if chunk.Len()+len(section) > 3000 {
			if _, err := m.ReplyText(c, chunk.String(), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true}); err != nil {
				return err
			}
			chunk.Reset()
		}
		if chunk.Len() > 0 {
			chunk.WriteString("\n\n")
		}
		chunk.WriteString(section)
	}
	_, err := m.ReplyText(c, chunk.String(), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Reload(c *gotdbot.Client, m *gotdbot.Message) error {
	if m.IsPrivate() {
		return nil
	}

	if expiry, ok := h.ReloadRateLimit.Get(m.ChatId); ok {
		remaining := time.Until(expiry)
		if remaining > 0 {
			minutes := int(math.Ceil(remaining.Minutes()))
			_, _ = m.ReplyText(c, fmt.Sprintf("Please wait %d minutes before reloading again.", minutes), nil)
			return nil
		}
	}

	member, err := c.GetChatMember(m.ChatId, &gotdbot.MessageSenderUser{UserId: m.SenderID()})
	if err != nil {
		_, _ = m.ReplyText(c, "Could not verify Telegram administrator permissions. Check the bot’s access and try again.", nil)
		return nil
	}

	status := member.Status.GetType()
	isAdmin := status == "chatMemberStatusAdministrator" || status == "chatMemberStatusCreator"

	if !isAdmin {
		_, _ = m.ReplyText(c, "Telegram administrator access is required. Ask an administrator to run /reload.", nil)
		return nil
	}

	h.AdminCache.Delete(m.ChatId)
	expiry := time.Now().Add(10 * time.Minute)
	h.ReloadRateLimit.Set(m.ChatId, expiry, 10*time.Minute)
	_, err = m.ReplyText(c, "Administrator permissions will be refreshed on the next check.", nil)
	return err
}

func (h *CommandHandler) Privacy(c *gotdbot.Client, m *gotdbot.Message) error {
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

<b>4. Data Control &amp; Rights</b>
• <b>Disconnect:</b> Use /disconnect to remove the bot’s stored authorization. Revoke the OAuth grant on GitHub to invalidate it there.
• <b>Removal:</b> You can remove repositories using /removerepo. To request deletion of stored records, contact the developer.

<b>5. Contact</b>
If you have questions or concerns, please visit our <a href="https://github.com/AshokShau/GithubBot">GitHub repository</a> or join our <a href="https://t.me/GuardxSupport">Telegram Support Group</a>.`

	_, err := m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Logout(c *gotdbot.Client, m *gotdbot.Message) error {
	err := h.DB.ClearUserToken(context.Background(), m.SenderID())
	if err != nil {
		_, err = m.ReplyText(c, "Could not disconnect your account. Please try again.", nil)
		return err
	}
	_, err = m.ReplyText(c, "GitHub disconnected. Use /connect to reconnect.", nil)
	return err
}

func (h *CommandHandler) handleAuthError(c *gotdbot.Client, m *gotdbot.Message, err error) bool {
	if errResp, ok := errors.AsType[*github.ErrorResponse](err); ok {
		if errResp.Response != nil && errResp.Response.StatusCode == http.StatusUnauthorized {
			_ = h.DB.ClearUserToken(context.Background(), m.SenderID())
			msg := "<b>GitHub authentication failed.</b>\nIt seems your token has expired or was revoked. Please /connect again."
			_, _ = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
			return true
		}
	}
	return false
}

func (h *CommandHandler) Close(c *gotdbot.Client, m *gotdbot.Message) error {
	return h.handleIssueAction(c, m, "closed")
}

func (h *CommandHandler) Reopen(c *gotdbot.Client, m *gotdbot.Message) error {
	return h.handleIssueAction(c, m, "open")
}

func (h *CommandHandler) Approve(c *gotdbot.Client, m *gotdbot.Message) error {
	if m.ReplyToMessageID() == 0 {
		_, err := m.ReplyText(c, "Please use this command in reply to a notification.", nil)
		return err
	}

	key := fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())
	mContext, found := h.ContextCache.Get(key)
	if !found {
		_, err := m.ReplyText(c, "This interaction has expired. Reply to a recent notification or open the resource on GitHub.", nil)
		return err
	}

	if !strings.HasPrefix(mContext.Type, "pr") {
		_, err := m.ReplyText(c, "This command requires a pull request. Reply to a recent pull request notification.", nil)
		return err
	}

	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	review := &github.PullRequestReviewRequest{
		Event: new("APPROVE"),
	}
	_, _, err = client.PullRequests.CreateReview(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, review)

	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to approve: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to approve", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Pull request %s/%s #%d approved.", mContext.Owner, mContext.Repo, mContext.IssueNumber), nil)
	return err
}

func (h *CommandHandler) handleIssueAction(c *gotdbot.Client, m *gotdbot.Message, state string) error {
	if m.ReplyToMessageID() == 0 {
		_, err := m.ReplyText(c, "Please use this command in reply to a notification.", nil)
		return err
	}

	key := fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())
	mContext, found := h.ContextCache.Get(key)
	if !found {
		_, err := m.ReplyText(c, "This interaction has expired. Reply to a recent notification or open the resource on GitHub.", nil)
		return err
	}

	if mContext.IssueNumber <= 0 || mContext.Type == "discussion" || mContext.Type == "discussion_comment" || mContext.Type == "workflow" {
		_, err := m.ReplyText(c, "This action requires an issue or pull request. Reply to a recent issue or pull request notification.", nil)
		return err
	}
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	req := github.UpdateIssueRequest{State: &state}
	_, _, err = client.Issues.Update(context.Background(), mContext.Owner, mContext.Repo, mContext.IssueNumber, req)

	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to update state: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to update state", err), nil)
		return nil
	}

	action := "closed"
	if state == "open" {
		action = "reopened"
	}
	_, err = m.ReplyText(c, fmt.Sprintf("%s/%s #%d %s.", mContext.Owner, mContext.Repo, mContext.IssueNumber, action), nil)
	return err
}

func (h *CommandHandler) getAuthenticatedClient(c *gotdbot.Client, m *gotdbot.Message) (*github.Client, error) {
	user, err := h.DB.GetUserByTelegramID(context.Background(), m.SenderID())
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		c.Logger.Warnf("Could not load your GitHub connection: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Could not load your GitHub connection", err), nil)
		return nil, err
	}
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		msg := "Connect your GitHub account first. Use /connect."
		_, _ = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil, fmt.Errorf("auth required")
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = m.ReplyText(c, "GitHub authorization is unavailable. Use /connect to reconnect.", nil)
		return nil, err
	}

	client, clientErr := h.ClientFactory.GetUserClient(context.Background(), token)
	if clientErr != nil {
		c.Logger.Warnf("GitHub is unavailable: %s", utils.ErrorDetails(clientErr))
		_, _ = m.ReplyText(c, utils.OperationError("GitHub is unavailable", clientErr), nil)
	}
	return client, clientErr
}

func (h *CommandHandler) resolveRepoContext(m *gotdbot.Message) (owner string, repo string, err error) {
	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) > 1 {
		parts := strings.Split(args[1], "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	if m.ReplyToMessageID() != 0 {
		key := fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.Owner != "" && mContext.Repo != "" {
			return mContext.Owner, mContext.Repo, nil
		}
	}

	links, dbErr := h.DB.GetChatLinks(context.Background(), m.ChatId)
	if dbErr == nil && len(links) == 1 {
		parts := strings.Split(links[0].RepoFullName, "/")
		if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
			return parts[0], parts[1], nil
		}
	}

	return "", "", errors.New("No repository selected. Use owner/repo, reply to a recent notification, or link one with /addrepo.")
}

func (h *CommandHandler) resolveIssueOrPRNumber(m *gotdbot.Message) (int, error) {
	if m.ReplyToMessageID() != 0 {
		key := fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.IssueNumber != 0 && mContext.Type != "discussion" && mContext.Type != "discussion_comment" {
			return mContext.IssueNumber, nil
		}
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) > 1 {
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "#") {
				arg = arg[1:]
			}
			var num int
			if parsed, err := strconv.Atoi(arg); err == nil && parsed > 0 {
				num = parsed
				return num, nil
			}
		}
	}

	return 0, errors.New("No issue or pull request selected. Reply to a recent notification or provide a positive issue or PR number.")
}

func (h *CommandHandler) resolveCommentID(m *gotdbot.Message) (int64, error) {
	if m.ReplyToMessageID() != 0 {
		key := fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())
		mContext, found := h.ContextCache.Get(key)
		if found && mContext.CommentID != 0 {
			return mContext.CommentID, nil
		}
	}
	return 0, errors.New("No comment selected. Reply to a recent comment notification.")
}

func (h *CommandHandler) Me(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	user, _, err := client.Users.Get(context.Background(), "")
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch user details: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch user details", err), nil)
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
		utils.Link("View profile", user.GetHTMLURL()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Repo(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch repository info: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch repository info", err), nil)
		return nil
	}

	prs, resp, err := client.PullRequests.List(context.Background(), owner, repo, &github.PullRequestListOptions{
		State:   "open",
		PerPage: 1,
	})
	var openPRs int
	if err == nil {
		if resp != nil && resp.LastPage > 0 {
			openPRs = resp.LastPage
		} else {
			openPRs = len(prs)
		}
	}

	openIssues := max(repoInfo.GetOpenIssuesCount()-openPRs, 0)
	openPRsText, openIssuesText := "Unavailable", "Unavailable"
	if err == nil {
		openPRsText = strconv.Itoa(openPRs)
		openIssuesText = strconv.Itoa(openIssues)
	} else {
		c.Logger.Warnf("Fetch repository pull request counts: %s", utils.ErrorDetails(err))
	}

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
			"• <b>Open Issues:</b> %s\n"+
			"• <b>Open PRs:</b> %s\n"+
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
		openIssuesText,
		openPRsText,
		html.EscapeString(license),
		html.EscapeString(language),
		utils.Link("View repository", repoInfo.GetHTMLURL()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Star(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Activity.Star(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to star repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to star repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Starred repository <b>%s/%s</b>!", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Unstar(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Activity.Unstar(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unstar repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unstar repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Removed star from <b>%s/%s</b>.", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Watch(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	sub := &github.Subscription{Subscribed: new(true)}
	_, _, err = client.Activity.SetRepositorySubscription(context.Background(), owner, repo, sub)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to watch repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to watch repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Watching repository <b>%s/%s</b>!", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Unwatch(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Activity.DeleteRepositorySubscription(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unwatch repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unwatch repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Stopped watching <b>%s/%s</b>.", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Fork(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	forked, _, err := client.Repositories.CreateFork(context.Background(), owner, repo, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fork repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fork repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Repository forked successfully to <b>%s</b>!", html.EscapeString(forked.GetFullName())), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Archive(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	update := &github.Repository{Archived: new(true)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to archive repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to archive repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Repository <b>%s/%s</b> archived.", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Unarchive(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	update := &github.Repository{Archived: new(false)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unarchive repository: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unarchive repository", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Repository <b>%s/%s</b> unarchived.", html.EscapeString(owner), html.EscapeString(repo)), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Contributors(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	contribs, _, err := client.Repositories.ListContributors(context.Background(), owner, repo, &github.ListContributorsOptions{
		PerPage: 10,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list contributors: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list contributors", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Top Contributors for %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if len(contribs) == 0 {
		msg.WriteString("No contributors found.")
	}
	for i, c := range contribs {
		msg.WriteString(fmt.Sprintf("%d. <b>%s</b> (%d commits)\n", i+1, html.EscapeString(c.GetLogin()), c.GetContributions()))
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Languages(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	langs, _, err := client.Repositories.ListLanguages(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch languages: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch languages", err), nil)
		return nil
	}

	var total int64
	for _, bytes := range langs {
		total += int64(bytes)
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Language Statistics for %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if total == 0 {
		msg.WriteString("No languages detected.")
	} else {
		for lang, bytes := range langs {
			pct := (float64(bytes) / float64(total)) * 100.0
			msg.WriteString(fmt.Sprintf("• <b>%s</b>: %.2f%%\n", html.EscapeString(lang), pct))
		}
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Branches(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	branches, _, err := client.Repositories.ListBranches(context.Background(), owner, repo, &github.BranchListOptions{
		PerPage: 20,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list branches: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list branches", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Branches for %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if len(branches) == 0 {
		msg.WriteString("No branches found.")
	}
	for _, br := range branches {
		msg.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(br.GetName())))
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Branch(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/branch branch-name</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	branchName := args[1]

	branch, _, err := client.Repositories.GetBranch(context.Background(), owner, repo, branchName, 3)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Branch not found or failed to fetch: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Branch not found or failed to fetch", err), nil)
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
		html.EscapeString(owner), html.EscapeString(repo),
		html.EscapeString(branch.GetName()),
		protected,
		html.EscapeString(sha),
		html.EscapeString(branch.GetCommit().GetCommit().GetAuthor().GetName()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Default(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/default branch-name</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	branchName := args[1]

	update := &github.Repository{DefaultBranch: new(branchName)}
	_, _, err = client.Repositories.Edit(context.Background(), owner, repo, update)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to change default branch: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to change default branch", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Default branch for <b>%s/%s</b> changed to <b>%s</b>.", html.EscapeString(owner), html.EscapeString(repo), html.EscapeString(branchName)), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Issue(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	text := m.GetText()
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
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/issue Title</code>\n\n[Optional Body]", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
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
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to create issue: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to create issue", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Created issue <b>#%d</b>: <a href=\"%s\">%s</a>", issue.GetNumber(), html.EscapeString(issue.GetHTMLURL()), html.EscapeString(issue.GetTitle()))+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Comment(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	cmdText := m.GetText()
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
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/comment comment-text (replying to a notification)</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}

	comment, _, err := client.Issues.CreateComment(context.Background(), owner, repo, num, github.IssueCommentRequest{Body: body})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to add comment: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to add comment", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Comment added to <b>#%d</b>: <a href=\"%s\">View</a>", num, html.EscapeString(comment.GetHTMLURL()))+"\n\nRepository: "+utils.Code(owner+"/"+repo), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Assign(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/assign @username</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.Issues.AddAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to assign user: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to assign user", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Assigned <b>@%s</b> to #%d.", html.EscapeString(target), num)+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) AssignMe(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	ghUser, _, err := client.Users.Get(context.Background(), "")
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch authenticated user details: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch authenticated user details", err), nil)
		return nil
	}

	target := ghUser.GetLogin()
	_, _, err = client.Issues.AddAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		c.Logger.Warnf("Failed to assign you: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to assign you", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Assigned you (<b>@%s</b>) to #%d.", html.EscapeString(target), num)+"\n\nRepository: "+utils.Code(owner+"/"+repo), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Unassign(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/unassign @username</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.Issues.RemoveAssignees(context.Background(), owner, repo, num, []string{target})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unassign user: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unassign user", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Unassigned <b>@%s</b> from #%d.", html.EscapeString(target), num)+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Label(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/label +bug -help-wanted</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
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
			if h.handleAuthError(c, m, err) {
				return nil
			}
			c.Logger.Warnf("Failed to add labels: %s", utils.ErrorDetails(err))
			_, _ = m.ReplyText(c, utils.OperationError("Failed to add labels", err), nil)
			return nil
		}
	}

	if len(toRemove) > 0 {
		for _, l := range toRemove {
			_, err = client.Issues.RemoveLabelForIssue(context.Background(), owner, repo, num, l)
			if err != nil {
				c.Logger.Warnf("Some labels could not be removed; earlier changes may have succeeded: %s", utils.ErrorDetails(err))
				_, _ = m.ReplyText(c, utils.OperationError("Some labels could not be removed; earlier changes may have succeeded", err), nil)
				return nil
			}
		}
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Labels updated for #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Labels(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	var labels []*github.Label
	if err == nil {
		labels, _, err = client.Issues.ListLabelsByIssue(context.Background(), owner, repo, num, nil)
	} else {
		labels, _, err = client.Issues.ListLabels(context.Background(), owner, repo, nil)
	}

	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list labels: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list labels", err), nil)
		return nil
	}

	var msg strings.Builder
	if num > 0 {
		msg.WriteString(fmt.Sprintf("<b>Labels for #%d:</b>\n\n", num))
	} else {
		msg.WriteString(fmt.Sprintf("<b>Labels for %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	}

	if len(labels) == 0 {
		msg.WriteString("No labels found.")
	} else {
		for _, l := range labels {
			msg.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(l.GetName())))
		}
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Milestone(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/milestone v1.0</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	mName := args[1]

	milestones, _, err := client.Issues.ListMilestones(context.Background(), owner, repo, &github.MilestoneListOptions{State: "open"})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch milestones: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch milestones", err), nil)
		return nil
	}

	var mNum *int
	for _, m := range milestones {
		if strings.EqualFold(m.GetTitle(), mName) {
			mNum = new(m.GetNumber())
			break
		}
	}

	if mNum == nil {
		_, _ = m.ReplyText(c, utils.Clip(fmt.Sprintf("Milestone '%s' not found. Check the open milestones on GitHub and try again.", mName), utils.MessageLimit), nil)
		return nil
	}

	req := github.UpdateIssueRequest{Milestone: mNum}
	_, _, err = client.Issues.Update(context.Background(), owner, repo, num, req)
	if err != nil {
		c.Logger.Warnf("Failed to assign milestone: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to assign milestone", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Assigned milestone <b>%s</b> to #%d.", html.EscapeString(mName), num)+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Lock(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Issues.Lock(context.Background(), owner, repo, num, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to lock: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to lock", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Locked conversation on #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Unlock(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Issues.Unlock(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unlock: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unlock", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Unlocked conversation on #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Pin(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	req, err := client.NewRequest(context.Background(), "PUT", fmt.Sprintf("repos/%s/%s/issues/%d/pin", owner, repo, num), nil)
	if err == nil {
		_, err = client.Do(req, nil)
	}

	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to pin issue: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to pin issue", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Pinned issue #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Unpin(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	req, err := client.NewRequest(context.Background(), "DELETE", fmt.Sprintf("repos/%s/%s/issues/%d/pin", owner, repo, num), nil)
	if err == nil {
		_, err = client.Do(req, nil)
	}

	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to unpin issue: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to unpin issue", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Unpinned issue #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
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
	return doGraphQL(ctx, client, req, nil)
}

func (h *CommandHandler) RequestChanges(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	cmdText := m.GetText()
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
		Event: new("REQUEST_CHANGES"),
		Body:  new(body),
	}
	_, _, err = client.PullRequests.CreateReview(context.Background(), owner, repo, num, review)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to request changes: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to request changes", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Requested changes on PR #%d.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Merge(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	method := "merge"
	if len(args) > 1 {
		methodArg := strings.ToLower(args[1])
		if methodArg == "squash" || methodArg == "rebase" || methodArg == "merge" {
			method = methodArg
		} else {
			_, err = m.ReplyText(c, "Invalid merge strategy. Use /merge, /merge squash, /merge rebase, or /merge merge.", nil)
			return err
		}
	}

	mergeResult, _, err := client.PullRequests.Merge(context.Background(), owner, repo, num, "", &github.PullRequestOptions{MergeMethod: method})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to merge PR: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to merge PR", err), nil)
		return nil
	}

	if mergeResult == nil || !mergeResult.GetMerged() {
		_, err = m.ReplyText(c, "Pull request was not merged. Check merge requirements on GitHub, then try again.", nil)
		return err
	}
	_, err = m.ReplyText(c, fmt.Sprintf("Merged PR #%d using strategy: <b>%s</b>.", num, method)+"\n\nRepository: "+utils.Code(owner+"/"+repo), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Draft(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch PR: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch PR", err), nil)
		return nil
	}

	query := `mutation($id: ID!) { convertPullRequestToDraft(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	variables := map[string]any{"id": pr.GetNodeID()}

	err = h.executeGraphQL(context.Background(), client, query, variables)
	if err != nil {
		c.Logger.Warnf("Failed to convert PR to draft: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to convert PR to draft", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("PR #%d converted to Draft.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Ready(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch PR: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch PR", err), nil)
		return nil
	}

	query := `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`
	variables := map[string]any{"id": pr.GetNodeID()}

	err = h.executeGraphQL(context.Background(), client, query, variables)
	if err != nil {
		c.Logger.Warnf("Failed to mark PR as ready: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to mark PR as ready", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("PR #%d marked as Ready for Review.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) Checks(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	var sha string
	num, err := h.resolveIssueOrPRNumber(m)
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
		_, _ = m.ReplyText(c, "Could not find a head commit. Check that the repository has commits and your account has access.", nil)
		return nil
	}

	checks, _, err := client.Checks.ListCheckRunsForRef(context.Background(), owner, repo, sha, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list checks: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list checks", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Checks status for commit %s:</b>\n\n", html.EscapeString(utils.ShortSHA(sha))))
	if checks.GetTotal() == 0 {
		msg.WriteString("No check runs found.")
	} else {
		for _, run := range checks.CheckRuns {
			status := run.GetStatus()
			conclusion := run.GetConclusion()
			summary := status
			if conclusion != "" {
				summary += " · " + conclusion
			}
			msg.WriteString(fmt.Sprintf("• %s: %s\n", utils.Link(run.GetName(), run.GetHTMLURL()), html.EscapeString(summary)))
		}
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Files(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	files, _, err := client.PullRequests.ListFiles(context.Background(), owner, repo, num, &github.ListOptions{PerPage: 20})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list changed files: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list changed files", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Changed files in PR #%d:</b>\n\n", num))
	if len(files) == 0 {
		msg.WriteString("No changed files found.")
	}
	for _, f := range files {
		msg.WriteString(fmt.Sprintf("• %s (<b>+%d</b> / <b>-%d</b>)\n", html.EscapeString(f.GetFilename()), f.GetAdditions(), f.GetDeletions()))
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Diff(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch PR: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch PR", err), nil)
		return nil
	}

	msg := fmt.Sprintf(
		"<b>PR #%d Change Summary:</b>\n\n"+
			"<b>%d</b> files changed\n"+
			"<b>+%d</b> additions\n"+
			"<b>-%d</b> deletions",
		num, pr.GetChangedFiles(), pr.GetAdditions(), pr.GetDeletions(),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Reviews(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	reviews, _, err := client.PullRequests.ListReviews(context.Background(), owner, repo, num, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list reviews: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list reviews", err), nil)
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

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Mergeable(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	pr, _, err := client.PullRequests.Get(context.Background(), owner, repo, num)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch PR details: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch PR details", err), nil)
		return nil
	}

	status := "No conflicts"
	if pr.Mergeable != nil && !*pr.Mergeable {
		status = "Has conflicts "
	} else if pr.Mergeable == nil {
		status = "Unknown / Checking..."
	}

	msg := fmt.Sprintf(
		"<b>Merge Status for PR #%d:</b>\n\n"+
			"• <b>Mergeable:</b> %s\n"+
			"• <b>Mergeable State:</b> %s",
		num, status, html.EscapeString(pr.GetMergeableState()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) RequestReview(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/request @username</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	target := strings.TrimPrefix(args[1], "@")

	_, _, err = client.PullRequests.RequestReviewers(context.Background(), owner, repo, num, github.ReviewersRequest{
		Reviewers: []string{target},
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to request reviewer: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to request reviewer", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Review requested from <b>@%s</b> for PR #%d.", html.EscapeString(target), num)+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Commit(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/commit SHA</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	sha := args[1]

	commit, _, err := client.Repositories.GetCommit(context.Background(), owner, repo, sha, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch commit: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch commit", err), nil)
		return nil
	}

	msg := fmt.Sprintf(
		"<b>Commit Details (%s/%s):</b>\n\n"+
			"• <b>SHA:</b> %s\n"+
			"• <b>Author:</b> %s\n"+
			"• <b>Message:</b> %s\n"+
			"• <b>HTML URL:</b> %s",
		html.EscapeString(owner), html.EscapeString(repo),
		html.EscapeString(commit.GetSHA()),
		html.EscapeString(commit.GetCommit().GetAuthor().GetName()),
		html.EscapeString(commit.GetCommit().GetMessage()),
		utils.Link("View commit", commit.GetHTMLURL()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Commits(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	commits, _, err := client.Repositories.ListCommits(context.Background(), owner, repo, &github.CommitsListOptions{
		PerPage: 10,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list commits: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list commits", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Commits in %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if len(commits) == 0 {
		msg.WriteString("No commits found.")
	}
	for _, c := range commits {
		shortSHA := c.GetSHA()
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		msg.WriteString(fmt.Sprintf("• <code>%s</code> - %s by <b>%s</b>\n", html.EscapeString(shortSHA), html.EscapeString(strings.Split(c.GetCommit().GetMessage(), "\n")[0]), html.EscapeString(c.GetCommit().GetAuthor().GetName())))
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Compare(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 3 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/compare branch1 branch2</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	base := args[1]
	head := args[2]

	comp, _, err := client.Repositories.CompareCommits(context.Background(), owner, repo, base, head, nil)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to compare: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to compare", err), nil)
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
		html.EscapeString(owner), html.EscapeString(repo),
		html.EscapeString(comp.GetStatus()),
		comp.GetAheadBy(),
		comp.GetBehindBy(),
		comp.GetTotalCommits(),
		utils.Link("View comparison", comp.GetHTMLURL()),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Actions(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	runs, _, err := client.Actions.ListRepositoryWorkflowRuns(context.Background(), owner, repo, &github.ListWorkflowRunsOptions{
		PerPage: 10,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list workflow runs: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list workflow runs", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Workflow Runs for %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if runs.GetTotalCount() == 0 {
		msg.WriteString("No workflow runs found.")
	} else {
		for _, run := range runs.WorkflowRuns {
			status := run.GetStatus()
			conclusion := run.GetConclusion()
			summary := status
			if conclusion != "" {
				summary += " · " + conclusion
			}
			msg.WriteString(fmt.Sprintf("• %s #%d: %s\n", utils.Link(run.GetName(), run.GetHTMLURL()), run.GetRunNumber(), html.EscapeString(summary)))
		}
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) RunWorkflow(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/run workflow.yml [branch]</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
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
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to trigger workflow: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to trigger workflow", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Triggered workflow <b>%s</b> on branch <b>%s</b>.", html.EscapeString(workflowFile), html.EscapeString(ref))+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) RerunWorkflow(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveWorkflowRunID(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Actions.RerunWorkflowByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to rerun workflow: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to rerun workflow", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Workflow run #%d rerunning.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) CancelWorkflow(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveWorkflowRunID(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	_, err = client.Actions.CancelWorkflowRunByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to cancel workflow: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to cancel workflow", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, fmt.Sprintf("Workflow run #%d cancelled.", num)+"\nRepository: "+owner+"/"+repo, nil)
	return err
}

func (h *CommandHandler) WorkflowLogs(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveWorkflowRunID(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	run, _, err := client.Actions.GetWorkflowRunByID(context.Background(), owner, repo, int64(num))
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to get workflow run: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to get workflow run", err), nil)
		return nil
	}

	msg := fmt.Sprintf("<a href=\"%s\">Open workflow logs for run #%d</a>", html.EscapeString(run.GetHTMLURL()+"/jobs"), num)
	_, err = m.ReplyText(c, msg, &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Release(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) > 1 && strings.ToLower(args[1]) == "create" {
		if len(args) < 3 {
			_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/release create v1.0.0</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
			return nil
		}
		tagName := args[2]

		req := github.CreateReleaseRequest{
			TagName:              tagName,
			Name:                 new(tagName),
			GenerateReleaseNotes: new(true),
		}

		rel, _, err := client.Repositories.CreateRelease(context.Background(), owner, repo, req)
		if err != nil {
			if h.handleAuthError(c, m, err) {
				return nil
			}
			c.Logger.Warnf("Failed to create release: %s", utils.ErrorDetails(err))
			_, _ = m.ReplyText(c, utils.OperationError("Failed to create release", err), nil)
			return nil
		}

		_, err = m.ReplyText(c, utils.LimitHTML(fmt.Sprintf("Created release <b>%s</b>: <a href=\"%s\">View Release</a>", html.EscapeString(rel.GetTagName()), html.EscapeString(rel.GetHTMLURL()))+"\n\nRepository: "+utils.Code(owner+"/"+repo), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return err
	}

	rel, _, err := client.Repositories.GetLatestRelease(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch latest release or none found: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch latest release or none found", err), nil)
		return nil
	}

	name := rel.GetTagName()
	if rel.GetName() != "" {
		name = rel.GetName()
	}

	msg := fmt.Sprintf(
		"<b>Latest release</b>\n\n"+
			"<b>Repository:</b> %s\n"+
			"<b>Name:</b> %s\n"+
			"<b>Tag:</b> %s",
		utils.Code(owner+"/"+repo), html.EscapeString(utils.Clip(name, 240)), utils.Code(utils.Clip(rel.GetTagName(), 160)),
	)
	if published := rel.GetPublishedAt(); !published.IsZero() {
		msg += "\n<b>Published:</b> " + published.Format("2006-01-02 15:04 UTC")
	}
	msg += "\n\n" + utils.Link("View release on GitHub", rel.GetHTMLURL())

	if body := rel.GetBody(); strings.TrimSpace(body) != "" {
		richMarkdown := "<p>" + strings.ReplaceAll(msg, "\n", "<br>") + "</p>\n\n" + utils.Clip(body, 8000)
		_, err = m.ReplyRichMessageMarkdown(c, richMarkdown, nil)
		return err
	}

	_, err = m.ReplyRichMessageHTML(c, "<p>"+strings.ReplaceAll(msg, "\n", "<br>")+"</p>", nil)
	return err
}

func (h *CommandHandler) Changelog(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	tagName := "vNext"
	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) > 1 {
		tagName = args[1]
	}

	notesReq := github.GenerateNotesRequest{
		TagName: tagName,
	}

	notes, _, err := client.Repositories.GenerateReleaseNotes(context.Background(), owner, repo, notesReq)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to generate changelog: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to generate changelog", err), nil)
		return nil
	}

	header := "<b>Generated changelog</b>\n\n<b>Repository:</b> " + utils.Code(owner+"/"+repo) + "\n<b>Tag:</b> " + utils.Code(utils.Clip(tagName, 160)) + "\n\n" + utils.Link("View releases on GitHub", "https://github.com/"+owner+"/"+repo+"/releases")
	if strings.TrimSpace(notes.GetBody()) == "" {
		_, err = m.ReplyRichMessageHTML(c, "<p>"+strings.ReplaceAll(header, "\n", "<br>")+"</p><p>GitHub returned no release notes for this tag.</p>", nil)
		return err
	}
	richMarkdown := "<p>" + strings.ReplaceAll(header, "\n", "<br>") + "</p>\n\n" + utils.Clip(notes.GetBody(), 8000)
	_, err = m.ReplyRichMessageMarkdown(c, richMarkdown, nil)
	return err
}

func (h *CommandHandler) CreateDiscussion(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	text := m.GetText()
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
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/discussion Title</code>\n\n[Optional Body]", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
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
		c.Logger.Warnf("Failed to query repository details: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to query repository details", err), nil)
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

	err = doGraphQL(context.Background(), client, req, &respData)
	if err != nil {
		c.Logger.Warnf("Failed to execute GraphQL: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to execute GraphQL", err), nil)
		return nil
	}

	repoID := respData.Data.Repository.ID
	if repoID == "" {
		_, _ = m.ReplyText(c, "Repository unavailable. Check your access and enable Discussions in the repository settings.", nil)
		return nil
	}

	categories := respData.Data.Repository.DiscussionCategories.Nodes
	if len(categories) == 0 {
		_, _ = m.ReplyText(c, "No discussion categories found. Ensure Discussions are enabled on the repository settings.", nil)
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
		_, _ = m.ReplyText(c, "Could not prepare the discussion. Please try again.", nil)
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

	err = doGraphQL(context.Background(), client, reqCreate, &respCreate)
	if err != nil {
		c.Logger.Warnf("Failed to create discussion: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to create discussion", err), nil)
		return nil
	}

	disc := respCreate.Data.CreateDiscussion.Discussion
	if disc.Number == 0 || utils.SafeURL(disc.URL) == "" {
		_, err = m.ReplyText(c, "GitHub did not confirm discussion creation. Check the repository before trying again.", nil)
		return err
	}
	_, err = m.ReplyText(c, fmt.Sprintf("Created discussion <b>#%d</b>: <a href=\"%s\">View Discussion</a>", disc.Number, html.EscapeString(disc.URL))+"\n\nRepository: "+utils.Code(owner+"/"+repo), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Answered(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	commentID, err := h.resolveCommentID(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	queryComment := `query($owner: String!, $name: String!) {
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
		_, _ = m.ReplyText(c, "Could not load the discussion comment. Please try again.", nil)
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

	err = doGraphQL(context.Background(), client, req, &respData)
	if err != nil {
		c.Logger.Warnf("Failed to fetch comments: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch comments", err), nil)
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
		_, _ = m.ReplyText(c, "Comment not found among recent discussions. Open it on GitHub to mark it as an answer.", nil)
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
		_, _ = m.ReplyText(c, "Could not prepare the answer update. Please try again.", nil)
		return nil
	}

	err = doGraphQL(context.Background(), client, reqAnswer, nil)
	if err != nil {
		c.Logger.Warnf("Failed to mark comment as answer: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to mark comment as answer", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, "Discussion marked as answered!", nil)
	return err
}

func (h *CommandHandler) Find(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/find keyword</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s is:issue", keyword, owner, repo)
	result, _, err := client.Search.Issues(context.Background(), query, &github.SearchOptions{
		PerPage: 5,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Search failed: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Search failed", err), nil)
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

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) PRSearch(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/pr keyword</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s is:pr", keyword, owner, repo)
	result, _, err := client.Search.Issues(context.Background(), query, &github.SearchOptions{
		PerPage: 5,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("PR search failed: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("PR search failed", err), nil)
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

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) SearchCode(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	args := append([]string{"/"}, m.ArgsList()...)
	if len(args) < 2 {
		_, _ = m.ReplyText(c, "<b>Usage</b>\n\n<code>/search keyword</code>", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
		return nil
	}
	keyword := strings.Join(args[1:], " ")

	query := fmt.Sprintf("%s repo:%s/%s", keyword, owner, repo)
	result, _, err := client.Search.Code(context.Background(), query, &github.SearchOptions{
		PerPage: 5,
	})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Code search failed: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Code search failed", err), nil)
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

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
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

func (h *CommandHandler) Mute(c *gotdbot.Client, m *gotdbot.Message) error {
	threadID := getTopicID(m)
	err := h.DB.MuteThread(context.Background(), m.ChatId, threadID)
	if err != nil {
		_, _ = m.ReplyText(c, "Could not mute this topic. Please try again.", nil)
		return nil
	}

	_, err = m.ReplyText(c, "This thread has been muted. You will no longer receive notifications here.", nil)
	return err
}

func (h *CommandHandler) Done(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	threadID, err := h.findNotificationThreadID(context.Background(), client, owner, repo, num)
	if err != nil {
		_, _ = m.ReplyText(c, "Could not find a corresponding GitHub notification for this issue/PR.", nil)
		return nil
	}

	_, err = client.Activity.MarkThreadDone(context.Background(), threadID)
	if err != nil {
		c.Logger.Warnf("Failed to mark as done: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to mark as done", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, "Marked notification as done.", nil)
	return err
}

func (h *CommandHandler) Read(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	num, err := h.resolveIssueOrPRNumber(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	threadID, err := h.findNotificationThreadID(context.Background(), client, owner, repo, num)
	if err != nil {
		_, _ = m.ReplyText(c, "Could not find a corresponding GitHub notification for this issue/PR.", nil)
		return nil
	}

	_, err = client.Activity.MarkThreadRead(context.Background(), threadID)
	if err != nil {
		c.Logger.Warnf("Failed to mark as read: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to mark as read", err), nil)
		return nil
	}

	_, err = m.ReplyText(c, "Marked notification as read.", nil)
	return err
}

func (h *CommandHandler) Stats(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	repoInfo, _, err := client.Repositories.Get(context.Background(), owner, repo)
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to fetch statistics: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to fetch statistics", err), nil)
		return nil
	}

	prs, prResp, err := client.PullRequests.List(context.Background(), owner, repo, &github.PullRequestListOptions{
		State:   "open",
		PerPage: 1,
	})
	var openPRs int
	if err == nil {
		if prResp != nil && prResp.LastPage > 0 {
			openPRs = prResp.LastPage
		} else {
			openPRs = len(prs)
		}
	}

	openIssues := max(repoInfo.GetOpenIssuesCount()-openPRs, 0)
	openPRsText, openIssuesText := "Unavailable", "Unavailable"
	if err == nil {
		openPRsText = strconv.Itoa(openPRs)
		openIssuesText = strconv.Itoa(openIssues)
	} else {
		c.Logger.Warnf("Fetch repository pull request counts: %s", utils.ErrorDetails(err))
	}

	contribs, _, contribErr := client.Repositories.ListContributors(context.Background(), owner, repo, &github.ListContributorsOptions{
		PerPage: 100,
	})
	contribsCount := "Unavailable"
	if contribErr == nil {
		contribsCount = strconv.Itoa(len(contribs))
	} else {
		c.Logger.Warnf("Fetch contributors: %s", utils.ErrorDetails(contribErr))
	}

	latestRelease := "N/A"
	rel, _, relErr := client.Repositories.GetLatestRelease(context.Background(), owner, repo)
	if relErr == nil {
		latestRelease = rel.GetTagName()
	}

	lastCommit := "N/A"
	commits, _, commitErr := client.Repositories.ListCommits(context.Background(), owner, repo, &github.CommitsListOptions{
		PerPage: 1,
	})
	if commitErr == nil && len(commits) > 0 {
		lastCommit = fmt.Sprintf("%s (by %s)", utils.ShortSHA(commits[0].GetSHA()), commits[0].GetCommit().GetAuthor().GetName())
	}

	msg := fmt.Sprintf(
		"<b>Repository Statistics for %s/%s:</b>\n\n"+
			"<b>Stars:</b> %d\n"+
			"<b>Forks:</b> %d\n"+
			"<b>Open Issues:</b> %s\n"+
			"<b>Open PRs:</b> %s\n"+
			"<b>Contributors (first 100):</b> %s\n"+
			"<b>Latest Release:</b> %s\n"+
			"<b>Last Commit:</b> %s",
		html.EscapeString(owner), html.EscapeString(repo),
		repoInfo.GetStargazersCount(),
		repoInfo.GetForksCount(),
		openIssuesText,
		openPRsText,
		contribsCount,
		html.EscapeString(latestRelease),
		html.EscapeString(lastCommit),
	)

	_, err = m.ReplyText(c, utils.LimitHTML(msg, utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

func (h *CommandHandler) Activity(c *gotdbot.Client, m *gotdbot.Message) error {
	client, err := h.getAuthenticatedClient(c, m)
	if err != nil {
		return nil
	}

	owner, repo, err := h.resolveRepoContext(m)
	if err != nil {
		_, _ = m.ReplyText(c, err.Error(), nil)
		return nil
	}

	events, _, err := client.Activity.ListRepositoryEvents(context.Background(), owner, repo, &github.ListOptions{PerPage: 10})
	if err != nil {
		if h.handleAuthError(c, m, err) {
			return nil
		}
		c.Logger.Warnf("Failed to list activity: %s", utils.ErrorDetails(err))
		_, _ = m.ReplyText(c, utils.OperationError("Failed to list activity", err), nil)
		return nil
	}

	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("<b>Recent Activity in %s/%s:</b>\n\n", html.EscapeString(owner), html.EscapeString(repo)))
	if len(events) == 0 {
		msg.WriteString("No recent activity found.")
	} else {
		for _, e := range events {
			evtType := e.GetType()
			evtType = strings.TrimSuffix(evtType, "Event")
			msg.WriteString(fmt.Sprintf("• <b>%s</b> by %s (%s)\n", html.EscapeString(evtType), html.EscapeString(e.GetActor().GetLogin()), e.GetCreatedAt().Format("2006-01-02 15:04")))
		}
	}

	_, err = m.ReplyText(c, utils.LimitHTML(msg.String(), utils.MessageLimit), &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML, DisableWebPagePreview: true})
	return err
}

// doGraphQL checks transport and GraphQL-level failures before reporting success.
func doGraphQL(ctx context.Context, client *github.Client, req *http.Request, target any) error {
	var raw json.RawMessage
	if _, err := client.Do(req, &raw); err != nil {
		return err
	}
	var envelope struct {
		Errors []json.RawMessage `json:"errors"`
		Data   json.RawMessage   `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 || len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return errors.New("GitHub rejected the GraphQL operation")
	}
	if target != nil {
		return json.Unmarshal(raw, target)
	}
	return nil
}

func (h *CommandHandler) resolveWorkflowRunID(m *gotdbot.Message) (int64, error) {
	if m.ReplyToMessageID() != 0 {
		if ctx, ok := h.ContextCache.Get(fmt.Sprintf("%d:%d", m.ChatId, m.ReplyToMessageID())); ok && ctx.RunID > 0 {
			return ctx.RunID, nil
		}
	}
	for _, arg := range m.ArgsList() {
		if id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64); err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, errors.New("No workflow run selected. Reply to a recent workflow notification or provide its run ID.")
}
