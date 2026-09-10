package callbacks

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"net/http"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v91/github"
)

type CallbackHandler struct {
	Config        *config.Config
	DB            *db.DB
	ClientFactory *github.ClientFactory
	EncryptionKey string
	ActionCache   *cache.Cache[string, models.PRActionContext]
	AdminCache    *cache.Cache[int64, []int64]
}

func NewCallbackHandler(cfg *config.Config, database *db.DB, factory *github.ClientFactory, key string, actionCache *cache.Cache[string, models.PRActionContext], adminCache *cache.Cache[int64, []int64]) *CallbackHandler {
	return &CallbackHandler{
		Config:        cfg,
		DB:            database,
		ClientFactory: factory,
		EncryptionKey: key,
		ActionCache:   actionCache,
		AdminCache:    adminCache,
	}
}

// Event aliases to compress callback data
var eventToShort = map[string]string{}
var shortToEvent = map[string]string{}

func init() {
	for _, e := range github.SupportedEvents {
		eventToShort[e.Name] = e.Short
		shortToEvent[e.Short] = e.Name
	}
}

func (h *CallbackHandler) HandleSettings(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
	if !u.IsPrivate() && !utils.IsAdmin(c, u.ChatId, u.SenderUserId, h.AdminCache) {
		_ = u.Answer(c, 0, true, "Only admins can change settings", "")
		return nil
	}

	data := u.DataString()
	parts := strings.Split(data, ":")

	if len(parts) < 2 {
		return nil
	}

	prefix := parts[0] // c, conf
	action := parts[1] // ls, r, te, ep

	if prefix == "c" {
		if action == "ls" {
			return h.showRepoList(c, u)
		}
		if action == "ar" {
			if len(parts) < 3 {
				return nil
			}
			subAction := parts[2]
			if subAction == "pg" {
				page, _ := strconv.Atoi(parts[3])
				return h.handleRepoPage(c, u, page)
			}
			if subAction == "id" {
				repoID, _ := strconv.ParseInt(parts[3], 10, 64)
				return h.handleAddRepoByID(c, u, repoID)
			}
		}

		if len(parts) < 3 {
			return nil
		}

		repoName := parts[2]
		link, err := h.DB.GetRepoLink(context.Background(), u.ChatId, repoName)
		if err != nil {
			_ = u.Answer(c, 0, false, "Repo not found", "")
			return nil
		}

		if action == "r" {
			// c:r:repo
			return h.showRepoMenu(c, u, link)
		}

		if action == "te" && len(parts) >= 4 {
			// c:te:repo:shortEvt:page
			shortEvt := parts[3]
			page := 1
			if len(parts) == 5 {
				page, _ = strconv.Atoi(parts[4])
			}

			evt, ok := shortToEvent[shortEvt]
			if !ok {
				evt = shortEvt
			}

			user, uErr := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
			if uErr != nil || user.EncryptedOAuthToken == "" {
				_ = u.Answer(c, 0, true, "Please /connect to GitHub first.", "")
				return nil
			}
			token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			if tErr != nil {
				_ = u.Answer(c, 0, true, "Auth error.", "")
				return nil
			}

			ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
			if err != nil {
				_ = u.Answer(c, 0, true, "Failed to create GitHub client.", "")
				return nil
			}
			repoParts := strings.Split(link.RepoFullName, "/")
			if len(repoParts) != 2 {
				return nil
			}
			owner, repoName := repoParts[0], repoParts[1]

			hook, _, hErr := ghClient.Repositories.GetHook(context.Background(), owner, repoName, link.WebhookID)
			if hErr != nil {
				if h.handleAuthError(c, u, hErr) {
					return nil
				}
				_ = u.Answer(c, 0, true, "Failed to fetch GitHub settings.", "")
				return nil
			}

			var currentEvents []string
			hasWildcard := false
			for _, e := range hook.Events {
				if e == "*" {
					hasWildcard = true
					break
				}
				currentEvents = append(currentEvents, e)
			}

			if hasWildcard {
				for _, se := range github.SupportedEvents {
					currentEvents = append(currentEvents, se.Name)
				}
			}

			found := false
			var newEvents []string
			for _, e := range currentEvents {
				if e == evt {
					found = true
				} else {
					newEvents = append(newEvents, e)
				}
			}
			if !found {
				newEvents = append(newEvents, evt)
			}

			hook.Events = newEvents
			_, _, editErr := ghClient.Repositories.EditHook(context.Background(), owner, repoName, link.WebhookID, hook)
			if editErr != nil {
				if h.handleAuthError(c, u, editErr) {
					return nil
				}
				_ = u.Answer(c, 0, true, "Failed to update GitHub.", "")
				return nil
			}

			return h.showIndividualEvents(c, u, link, page)
		} else if action == "ep" && len(parts) == 4 {
			// c:ep:repo:page
			page, _ := strconv.Atoi(parts[3])
			return h.showIndividualEvents(c, u, link, page)
		} else if action == "presets" && len(parts) >= 3 {
			// c:presets:repo:mode
			// mode: push, all
			if len(parts) < 4 {
				return nil
			}
			mode := parts[3]
			return h.handlePresets(c, u, link, mode)
		} else if action == "iev" && len(parts) == 4 {
			// c:iev:repo:page
			page, _ := strconv.Atoi(parts[3])
			return h.showIndividualEvents(c, u, link, page)
		}
	}

	return nil
}

func (h *CallbackHandler) showRepoMenu(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, l *models.RepoLink) error {
	kb := &gotdbot.ReplyMarkupInlineKeyboard{
		Rows: [][]gotdbot.InlineKeyboardButton{
			{
				{Text: "Just the push event", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:presets:%s:push", l.RepoFullName)}},
			},
			{
				{Text: "Send me everything", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:presets:%s:all", l.RepoFullName)}},
			},
			{
				{Text: "Let me select individual events", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:iev:%s:1", l.RepoFullName)}},
			},
			{
				{Text: "🔙 Back to Repo List", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: []byte("c:ls")}},
			},
		},
	}

	_, err := u.EditMessageText(c, fmt.Sprintf("Configuration for <b>%s</b>:", l.RepoFullName), &gotdbot.EditTextMessageOpts{
		ReplyMarkup: kb,
		ParseMode:   gotdbot.ParseModeHTML,
	})
	return err
}

func (h *CallbackHandler) handlePresets(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, l *models.RepoLink, mode string) error {
	user, uErr := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if uErr != nil || user.EncryptedOAuthToken == "" {
		_ = u.Answer(c, 0, true, "Please /connect to GitHub first.", "")
		return nil
	}

	token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if tErr != nil {
		_ = u.Answer(c, 0, true, "Auth error.", "")
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, 0, true, "Failed to create GitHub client.", "")
		return nil
	}
	repoParts := strings.Split(l.RepoFullName, "/")
	if len(repoParts) != 2 {
		return nil
	}
	owner, repoName := repoParts[0], repoParts[1]

	hook, _, hErr := ghClient.Repositories.GetHook(context.Background(), owner, repoName, l.WebhookID)
	if hErr != nil {
		if h.handleAuthError(c, u, hErr) {
			return nil
		}
		_ = u.Answer(c, 0, true, "Failed to fetch GitHub hook.", "")
		return nil
	}

	var newEvents []string
	if mode == "push" {
		newEvents = []string{"push"}
	} else if mode == "all" {
		newEvents = []string{"*"}
	} else {
		return nil
	}

	hook.Events = newEvents
	_, _, editErr := ghClient.Repositories.EditHook(context.Background(), owner, repoName, l.WebhookID, hook)
	if editErr != nil {
		if h.handleAuthError(c, u, editErr) {
			return nil
		}
		_ = u.Answer(c, 0, true, "Failed to update GitHub hook.", "")
		return nil
	}

	responseText := "✅ <b>Success!</b> I've updated the repository settings to send <b>everything</b>."
	if mode == "push" {
		responseText = "✅ <b>Success!</b> I've updated the repository settings to send <b>push events only</b>."
	}

	kb := &gotdbot.ReplyMarkupInlineKeyboard{
		Rows: [][]gotdbot.InlineKeyboardButton{
			{{Text: "🔙 Back", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:r:%s", l.RepoFullName)}}},
		},
	}

	_, err = u.EditMessageText(c, responseText, &gotdbot.EditTextMessageOpts{
		ReplyMarkup: kb,
		ParseMode:   gotdbot.ParseModeHTML,
	})
	return err
}

func (h *CallbackHandler) showIndividualEvents(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, l *models.RepoLink, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user.EncryptedOAuthToken == "" {
		_, _ = u.EditMessageText(c, "Error: You must be connected to GitHub to view/edit settings.", nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = u.EditMessageText(c, "Auth error. Please reconnect.", nil)
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_, _ = u.EditMessageText(c, "Failed to create GitHub client.", nil)
		return nil
	}
	parts := strings.Split(l.RepoFullName, "/")
	if len(parts) != 2 {
		return nil
	}
	owner, repoName := parts[0], parts[1]

	hook, _, err := ghClient.Repositories.GetHook(context.Background(), owner, repoName, l.WebhookID)
	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		_, _ = u.EditMessageText(c, "Error fetching webhook settings from GitHub. Check permissions.", nil)
		return nil
	}

	enabledEvents := make(map[string]bool)
	if hook != nil {
		for _, e := range hook.Events {
			if e == "*" {
				for _, supported := range github.SupportedEvents {
					enabledEvents[supported.Name] = true
				}
				break
			}
			enabledEvents[e] = true
		}
	}

	var rows [][]gotdbot.InlineKeyboardButton
	var row []gotdbot.InlineKeyboardButton

	for _, e := range github.SupportedEvents {
		status := "❌"
		if enabledEvents[e.Name] {
			status = "✅"
		}

		cbData := fmt.Sprintf("c:te:%s:%s:%d", l.RepoFullName, e.Short, page)
		btnText := fmt.Sprintf("%s %s", status, e.Label)

		row = append(row, gotdbot.InlineKeyboardButton{
			Text: btnText,
			Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: []byte(cbData)},
		})

		if len(row) == 2 {
			rows = append(rows, row)
			row = []gotdbot.InlineKeyboardButton{}
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	webhookSettingsURL := fmt.Sprintf("https://github.com/%s/%s/settings/hooks/%d", owner, repoName, l.WebhookID)
	rows = append(rows, []gotdbot.InlineKeyboardButton{
		{Text: "🌐 Edit more on GitHub", Type: &gotdbot.InlineKeyboardButtonTypeUrl{Url: webhookSettingsURL}},
	})

	rows = append(rows, []gotdbot.InlineKeyboardButton{
		{Text: "🔙 Back", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:r:%s", l.RepoFullName)}},
	})

	kb := &gotdbot.ReplyMarkupInlineKeyboard{Rows: rows}

	_, err = u.EditMessageText(c, fmt.Sprintf("Individual Events for <b>%s</b>:", l.RepoFullName), &gotdbot.EditTextMessageOpts{
		ReplyMarkup: kb,
		ParseMode:   gotdbot.ParseModeHTML,
	})
	return err
}

func (h *CallbackHandler) showRepoList(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
	links, err := h.DB.GetChatLinks(context.Background(), u.ChatId)
	if err != nil {
		return err
	}

	if len(links) == 0 {
		_, err = u.EditMessageText(c, "No repositories linked. Use /addrepo first.", nil)
		return err
	}

	var rows [][]gotdbot.InlineKeyboardButton
	for _, l := range links {
		rows = append(rows, []gotdbot.InlineKeyboardButton{
			{Text: l.RepoFullName, Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:r:%s", l.RepoFullName)}},
		})
	}

	kb := &gotdbot.ReplyMarkupInlineKeyboard{Rows: rows}

	_, err = u.EditMessageText(c, "Select a repository to configure:", &gotdbot.EditTextMessageOpts{
		ReplyMarkup: kb,
	})
	return err
}

func (h *CallbackHandler) handleRepoPage(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user.EncryptedOAuthToken == "" {
		_ = u.Answer(c, 0, true, "Auth error. Please /connect again.", "")
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_ = u.Answer(c, 0, true, "Auth error.", "")
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, 0, true, "Failed to create GitHub client.", "")
		return nil
	}
	opts := &gh.RepositoryListOptions{
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 5, Page: page},
	}

	repos, resp, err := ghClient.Repositories.List(context.Background(), "", opts)
	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		_ = u.Answer(c, 0, true, "GitHub API error.", "")
		return nil
	}

	var rows [][]gotdbot.InlineKeyboardButton
	for _, repo := range repos {
		rows = append(rows, []gotdbot.InlineKeyboardButton{
			{Text: repo.GetFullName(), Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:id:%d", repo.GetID())}},
		})
	}

	var navRow []gotdbot.InlineKeyboardButton
	if resp.FirstPage != 0 && resp.PrevPage != 0 {
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: "< Prev", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", resp.PrevPage)}})
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
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: text, Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", i)}})
	}

	if resp.NextPage != 0 {
		navRow = append(navRow, gotdbot.InlineKeyboardButton{Text: "Next >", Type: &gotdbot.InlineKeyboardButtonTypeCallback{Data: fmt.Appendf(nil, "c:ar:pg:%d", resp.NextPage)}})
	}

	if len(navRow) > 0 {
		rows = append(rows, navRow)
	}

	kb := &gotdbot.ReplyMarkupInlineKeyboard{Rows: rows}

	_, err = u.EditMessageText(c, fmt.Sprintf("Select a repository to add (Page %d):", page), &gotdbot.EditTextMessageOpts{
		ReplyMarkup: kb,
	})

	return err
}

func (h *CallbackHandler) handleAddRepoByID(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, repoID int64) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil {
		return nil
	}

	token, _ := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, 0, true, "Failed to create GitHub client.", "")
		return nil
	}

	repo, _, err := ghClient.Repositories.GetByID(context.Background(), repoID)
	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		_ = u.Answer(c, 0, true, "Repo not found or access denied.", "")
		return nil
	}

	if existingLink, _ := h.DB.GetRepoLink(context.Background(), u.ChatId, repo.GetFullName()); existingLink != nil {
		_ = u.Answer(c, 0, true, fmt.Sprintf("Repository %s is already linked to this chat.", repo.GetFullName()), "")
		_, err = u.EditMessageText(c, fmt.Sprintf("Repository <b>%s</b> is already linked to this chat.", repo.GetFullName()), &gotdbot.EditTextMessageOpts{
			ParseMode: gotdbot.ParseModeHTML,
		})
		return err
	}

	chatToken, encErr := utils.Encrypt(fmt.Sprintf("%d", u.ChatId), h.EncryptionKey)
	if encErr != nil {
		_ = u.Answer(c, 0, true, "Error generating webhook token.", "")
		return nil
	}

	webhookURL := fmt.Sprintf("%s/webhook/%s", h.Config.TelegramWebhookURL, chatToken)
	webhookConfig := &gh.HookConfig{
		URL:         new(webhookURL),
		ContentType: new("json"),
		Secret:      new(h.Config.GitHubWebhookSecret),
	}

	var defaultEvents []string
	for _, e := range github.SupportedEvents {
		defaultEvents = append(defaultEvents, e.Name)
	}

	hook := &gh.Hook{
		Name:   new("web"),
		Events: defaultEvents,
		Config: webhookConfig,
		Active: new(true),
	}

	createdHook, _, hookErr := ghClient.Repositories.CreateHook(context.Background(), repo.GetOwner().GetLogin(), repo.GetName(), hook)
	if hookErr != nil {
		if h.handleAuthError(c, u, hookErr) {
			return nil
		}

		_, err = u.EditMessageText(c, fmt.Sprintf("Webhook creation failed: %v. Check permissions", hookErr), &gotdbot.EditTextMessageOpts{
			ParseMode: gotdbot.ParseModeHTML,
		})
		return err
	}

	webhookID := createdHook.GetID()
	link := models.RepoLink{
		RepoFullName: repo.GetFullName(),
		WebhookID:    webhookID,
	}

	err = h.DB.AddRepoLink(context.Background(), u.ChatId, link)
	if err != nil {
		_ = u.Answer(c, 0, false, "Error linking repository.", "")
		return nil
	}

	_, err = u.EditMessageText(c, fmt.Sprintf("✅ Repository <b>%s</b> linked successfully!", repo.GetFullName()), &gotdbot.EditTextMessageOpts{
		ParseMode: gotdbot.ParseModeHTML,
	})
	return err
}

func (h *CallbackHandler) HandlePRAction(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
	data := u.DataString()
	parts := strings.Split(data, ":") // act:approve:uuid

	if len(parts) != 3 {
		return nil
	}

	action := parts[1]
	actionID := parts[2]

	prContext, ok := h.ActionCache.Get(actionID)
	if !ok {
		_ = u.Answer(c, 0, true, "Action expired. Please open the PR link manually.", "")
		return nil
	}

	owner := prContext.Owner
	repo := prContext.Repo
	prNum := prContext.PRNumber

	repoFullName := fmt.Sprintf("%s/%s", owner, repo)
	_, err := h.DB.GetRepoLink(context.Background(), u.ChatId, repoFullName)
	if err != nil {
		_ = u.Answer(c, 0, true, "This chat is not linked to the repo.", "")
		return nil
	}

	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user.EncryptedOAuthToken == "" {
		_ = u.Answer(c, 0, true, "Please connect GitHub account first via /connect", "")
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_ = u.Answer(c, 0, true, "Auth error. Reconnect via /connect", "")
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, 0, true, "Failed to create GitHub client.", "")
		return nil
	}
	ctxBg := context.Background()

	var msg string

	switch action {
	case "approve":
		_, _, err = ghClient.PullRequests.CreateReview(ctxBg, owner, repo, prNum, &gh.PullRequestReviewRequest{Event: new("APPROVE")})
		msg = "Approved!"
	case "close":
		_, _, err = ghClient.PullRequests.Edit(ctxBg, owner, repo, prNum, &gh.PullRequest{State: new("closed")})
		msg = "Closed!"
	}

	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		_ = u.Answer(c, 0, true, fmt.Sprintf("Failed: %v", err), "")
		return nil
	}

	_ = u.Answer(c, 0, true, msg, "")
	return nil
}

func (h *CallbackHandler) handleAuthError(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, err error) bool {
	if errResp, ok := errors.AsType[*gh.ErrorResponse](err); ok {
		if errResp.Response.StatusCode == http.StatusUnauthorized || errResp.Response.StatusCode == http.StatusForbidden {
			_ = h.DB.ClearUserToken(context.Background(), u.SenderUserId)
			_ = u.Answer(c, 0, true, "GitHub auth error. Token revoked or expired.", "")
			return true
		}
	}
	return false
}
