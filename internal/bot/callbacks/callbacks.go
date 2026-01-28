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

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	gh "github.com/google/go-github/v82/github"
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

func (h *CallbackHandler) HandleSettings(b *gotgbot.Bot, ctx *ext.Context) error {
	if ctx.EffectiveChat.Type != gotgbot.ChatTypePrivate && !utils.IsAdmin(b, ctx.EffectiveChat.Id, ctx.EffectiveUser.Id, h.AdminCache) {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Only admins can change settings", ShowAlert: true})
		return nil
	}

	data := ctx.CallbackQuery.Data
	parts := strings.Split(data, ":")

	// c:ls -> conf:list
	// c:r -> conf:repo
	// c:te -> conf:toggle_evt
	// c:ep -> conf:evt_pg

	if len(parts) < 2 {
		return nil
	}

	prefix := parts[0] // c, conf
	action := parts[1] // ls, r, te, ep

	if prefix == "c" {
		if action == "ls" {
			return h.showRepoList(b, ctx)
		}
		if action == "ar" {
			if len(parts) < 3 {
				return nil
			}
			subAction := parts[2]
			if subAction == "pg" {
				page, _ := strconv.Atoi(parts[3])
				return h.handleRepoPage(b, ctx, page)
			}
			if subAction == "id" {
				repoID, _ := strconv.ParseInt(parts[3], 10, 64)
				return h.handleAddRepoByID(b, ctx, repoID)
			}
		}

		if len(parts) < 3 {
			return nil
		}

		repoName := parts[2]
		link, err := h.DB.GetRepoLink(context.Background(), ctx.EffectiveChat.Id, repoName)
		if err != nil {
			_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Repo not found"})
			return nil
		}

		if action == "r" {
			// c:r:repo
			return h.showRepoMenu(b, ctx, link)
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

			user, uErr := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
			if uErr != nil || user.EncryptedOAuthToken == "" {
				_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Please /connect to GitHub first.", ShowAlert: true})
				return nil
			}
			token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			if tErr != nil {
				_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Auth error.", ShowAlert: true})
				return nil
			}

			client := h.ClientFactory.GetUserClient(context.Background(), token)
			repoParts := strings.Split(link.RepoFullName, "/")
			if len(repoParts) != 2 {
				return nil
			}
			owner, repoName := repoParts[0], repoParts[1]

			hook, _, hErr := client.Repositories.GetHook(context.Background(), owner, repoName, link.WebhookID)
			if hErr != nil {
				if h.handleAuthError(b, ctx, hErr) {
					return nil
				}
				_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Failed to fetch GitHub settings.", ShowAlert: true})
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
			_, _, editErr := client.Repositories.EditHook(context.Background(), owner, repoName, link.WebhookID, hook)
			if editErr != nil {
				if h.handleAuthError(b, ctx, editErr) {
					return nil
				}
				_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Failed to update GitHub.", ShowAlert: true})
				return nil
			}

			return h.showIndividualEvents(b, ctx, link, page)
		} else if action == "ep" && len(parts) == 4 {
			// c:ep:repo:page
			page, _ := strconv.Atoi(parts[3])
			return h.showIndividualEvents(b, ctx, link, page)
		} else if action == "presets" && len(parts) >= 3 {
			// c:presets:repo:mode
			// mode: push, all
			if len(parts) < 4 {
				return nil
			}
			mode := parts[3]
			return h.handlePresets(b, ctx, link, mode)
		} else if action == "iev" && len(parts) == 4 {
			// c:iev:repo:page
			page, _ := strconv.Atoi(parts[3])
			return h.showIndividualEvents(b, ctx, link, page)
		}
	}

	return nil
}

func (h *CallbackHandler) showRepoMenu(b *gotgbot.Bot, ctx *ext.Context, l *models.RepoLink) error {
	var kb [][]gotgbot.InlineKeyboardButton

	kb = append(kb, []gotgbot.InlineKeyboardButton{
		{Text: "Just the push event", CallbackData: fmt.Sprintf("c:presets:%s:push", l.RepoFullName)},
	})

	kb = append(kb, []gotgbot.InlineKeyboardButton{
		{Text: "Send me everything", CallbackData: fmt.Sprintf("c:presets:%s:all", l.RepoFullName)},
	})

	kb = append(kb, []gotgbot.InlineKeyboardButton{
		{Text: "Let me select individual events", CallbackData: fmt.Sprintf("c:iev:%s:1", l.RepoFullName)},
	})

	kb = append(kb, []gotgbot.InlineKeyboardButton{
		{Text: "🔙 Back to Repo List", CallbackData: "c:ls"},
	})

	_, _, err := ctx.EffectiveMessage.EditText(b, fmt.Sprintf("Configuration for <b>%s</b>:", l.RepoFullName), &gotgbot.EditMessageTextOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
		ParseMode:   "HTML",
	})
	return err
}

func (h *CallbackHandler) handlePresets(b *gotgbot.Bot, ctx *ext.Context, l *models.RepoLink, mode string) error {
	user, uErr := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if uErr != nil || user.EncryptedOAuthToken == "" {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Please /connect to GitHub first.", ShowAlert: true})
		return nil
	}

	token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if tErr != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Auth error.", ShowAlert: true})
		return nil
	}

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	repoParts := strings.Split(l.RepoFullName, "/")
	if len(repoParts) != 2 {
		return nil
	}
	owner, repoName := repoParts[0], repoParts[1]

	hook, _, hErr := client.Repositories.GetHook(context.Background(), owner, repoName, l.WebhookID)
	if hErr != nil {
		if h.handleAuthError(b, ctx, hErr) {
			return nil
		}
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Failed to fetch GitHub hook.", ShowAlert: true})
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
	_, _, editErr := client.Repositories.EditHook(context.Background(), owner, repoName, l.WebhookID, hook)
	if editErr != nil {
		if h.handleAuthError(b, ctx, editErr) {
			return nil
		}
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Failed to update GitHub hook.", ShowAlert: true})
		return nil
	}

	responseText := "✅ <b>Success!</b> I've updated the repository settings to send <b>everything</b>."
	if mode == "push" {
		responseText = "✅ <b>Success!</b> I've updated the repository settings to send <b>push events only</b>."
	}

	kb := [][]gotgbot.InlineKeyboardButton{
		{{Text: "🔙 Back", CallbackData: fmt.Sprintf("c:r:%s", l.RepoFullName)}},
	}

	_, _, err := ctx.EffectiveMessage.EditText(b, responseText, &gotgbot.EditMessageTextOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
		ParseMode:   "HTML",
	})
	return err
}

func (h *CallbackHandler) showIndividualEvents(b *gotgbot.Bot, ctx *ext.Context, l *models.RepoLink, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		_, _, _ = ctx.EffectiveMessage.EditText(b, "Error: You must be connected to GitHub to view/edit settings.", nil)
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _, _ = ctx.EffectiveMessage.EditText(b, "Auth error. Please reconnect.", nil)
		return nil
	}

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	parts := strings.Split(l.RepoFullName, "/")
	if len(parts) != 2 {
		return nil
	}
	owner, repoName := parts[0], parts[1]

	hook, _, err := client.Repositories.GetHook(context.Background(), owner, repoName, l.WebhookID)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _, _ = ctx.EffectiveMessage.EditText(b, "Error fetching webhook settings from GitHub. Check permissions.", nil)
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

	var kb [][]gotgbot.InlineKeyboardButton
	var row []gotgbot.InlineKeyboardButton

	for _, e := range github.SupportedEvents {
		status := "❌"
		if enabledEvents[e.Name] {
			status = "✅"
		}

		// c:te:repo:shortEvt:page
		cbData := fmt.Sprintf("c:te:%s:%s:%d", l.RepoFullName, e.Short, page)
		btnText := fmt.Sprintf("%s %s", status, e.Label)

		row = append(row, gotgbot.InlineKeyboardButton{Text: btnText, CallbackData: cbData})

		if len(row) == 2 {
			kb = append(kb, row)
			row = []gotgbot.InlineKeyboardButton{}
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}

	webhookSettingsURL := fmt.Sprintf("https://github.com/%s/%s/settings/hooks/%d", owner, repoName, l.WebhookID)
	kb = append(kb, []gotgbot.InlineKeyboardButton{
		{Text: "🌐 Edit more on GitHub", Url: webhookSettingsURL},
	})

	kb = append(kb, []gotgbot.InlineKeyboardButton{{Text: "🔙 Back", CallbackData: fmt.Sprintf("c:r:%s", l.RepoFullName)}})

	_, _, err = ctx.EffectiveMessage.EditText(b, fmt.Sprintf("Individual Events for <b>%s</b>:", l.RepoFullName), &gotgbot.EditMessageTextOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
		ParseMode:   "HTML",
	})
	return err
}

func (h *CallbackHandler) showRepoList(b *gotgbot.Bot, ctx *ext.Context) error {
	links, err := h.DB.GetChatLinks(context.Background(), ctx.EffectiveChat.Id)
	if err != nil {
		return err
	}

	if len(links) == 0 {
		_, _, err = ctx.EffectiveMessage.EditText(b, "No repositories linked. Use /addrepo first.", nil)
		return err
	}

	var kb [][]gotgbot.InlineKeyboardButton
	for _, l := range links {
		kb = append(kb, []gotgbot.InlineKeyboardButton{
			{Text: l.RepoFullName, CallbackData: fmt.Sprintf("c:r:%s", l.RepoFullName)},
		})
	}

	_, _, err = ctx.EffectiveMessage.EditText(b, "Select a repository to configure:", &gotgbot.EditMessageTextOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
	})
	return err
}

func (h *CallbackHandler) handleRepoPage(b *gotgbot.Bot, ctx *ext.Context, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Auth error. Please /connect again.", ShowAlert: true})
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Auth error.", ShowAlert: true})
		return nil
	}

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	opts := &gh.RepositoryListOptions{
		Sort:        "updated",
		Direction:   "desc",
		ListOptions: gh.ListOptions{PerPage: 5, Page: page},
	}

	repos, resp, err := client.Repositories.List(context.Background(), "", opts)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "GitHub API error.", ShowAlert: true})
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

	_, _, err = ctx.EffectiveMessage.EditText(b, fmt.Sprintf("Select a repository to add (Page %d):", page), &gotgbot.EditMessageTextOpts{
		ReplyMarkup: gotgbot.InlineKeyboardMarkup{InlineKeyboard: kb},
	})

	return err
}

func (h *CallbackHandler) handleAddRepoByID(b *gotgbot.Bot, ctx *ext.Context, repoID int64) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil {
		return nil
	}

	token, _ := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	client := h.ClientFactory.GetUserClient(context.Background(), token)

	repo, _, err := client.Repositories.GetByID(context.Background(), repoID)
	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Repo not found or access denied.", ShowAlert: true})
		return nil
	}

	chatToken, encErr := utils.Encrypt(fmt.Sprintf("%d", ctx.EffectiveChat.Id), h.EncryptionKey)
	if encErr != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Error generating webhook token.", ShowAlert: true})
		return nil
	}

	webhookURL := fmt.Sprintf("%s/webhook/%s", h.Config.TelegramWebhookURL, chatToken)
	webhookConfig := &gh.HookConfig{
		URL:         gh.String(webhookURL),
		ContentType: gh.String("json"),
		Secret:      gh.String(h.Config.GitHubWebhookSecret),
	}

	var defaultEvents []string
	for _, e := range github.SupportedEvents {
		defaultEvents = append(defaultEvents, e.Name)
	}

	hook := &gh.Hook{
		Name:   gh.String("web"),
		Events: defaultEvents,
		Config: webhookConfig,
		Active: gh.Bool(true),
	}

	createdHook, _, hookErr := client.Repositories.CreateHook(context.Background(), repo.GetOwner().GetLogin(), repo.GetName(), hook)
	if hookErr != nil {
		if h.handleAuthError(b, ctx, hookErr) {
			return nil
		}
		msg := fmt.Sprintf("Webhook creation failed: %v. Check permissions", hookErr)
		_, _, err = ctx.EffectiveMessage.EditText(b, msg, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
		return err
	}

	webhookID := createdHook.GetID()
	link := models.RepoLink{
		RepoFullName: repo.GetFullName(),
		WebhookID:    webhookID,
	}

	err = h.DB.AddRepoLink(context.Background(), ctx.EffectiveChat.Id, link)
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Error linking repository."})
		return nil
	}

	msg := fmt.Sprintf("✅ Repository <b>%s</b> linked successfully!", repo.GetFullName())
	_, _, err = ctx.EffectiveMessage.EditText(b, msg, &gotgbot.EditMessageTextOpts{ParseMode: "HTML"})
	return err
}

func (h *CallbackHandler) HandlePRAction(b *gotgbot.Bot, ctx *ext.Context) error {
	data := ctx.CallbackQuery.Data
	parts := strings.Split(data, ":") // act:approve:uuid

	if len(parts) != 3 {
		return nil
	}

	action := parts[1]
	actionID := parts[2]

	prContext, ok := h.ActionCache.Get(actionID)
	if !ok {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Action expired. Please open the PR link manually.", ShowAlert: true})
		return nil
	}

	owner := prContext.Owner
	repo := prContext.Repo
	prNum := prContext.PRNumber

	repoFullName := fmt.Sprintf("%s/%s", owner, repo)
	_, err := h.DB.GetRepoLink(context.Background(), ctx.EffectiveChat.Id, repoFullName)
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "This chat is not linked to the repo.", ShowAlert: true})
		return nil
	}

	user, err := h.DB.GetUserByTelegramID(context.Background(), ctx.EffectiveUser.Id)
	if err != nil || user.EncryptedOAuthToken == "" {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Please connect GitHub account first via /connect", ShowAlert: true})
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "Auth error. Reconnect via /connect", ShowAlert: true})
		return nil
	}

	client := h.ClientFactory.GetUserClient(context.Background(), token)
	ctxBg := context.Background()

	var msg string

	switch action {
	case "approve":
		_, _, err = client.PullRequests.CreateReview(ctxBg, owner, repo, prNum, &gh.PullRequestReviewRequest{Event: gh.String("APPROVE")})
		msg = "Approved!"
	case "close":
		state := "closed"
		_, _, err = client.PullRequests.Edit(ctxBg, owner, repo, prNum, &gh.PullRequest{State: &state})
		msg = "Closed!"
	}

	if err != nil {
		if h.handleAuthError(b, ctx, err) {
			return nil
		}
		_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: fmt.Sprintf("Failed: %v", err), ShowAlert: true})
		return nil
	}

	_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: msg, ShowAlert: true})
	return nil
}

func (h *CallbackHandler) handleAuthError(b *gotgbot.Bot, ctx *ext.Context, err error) bool {
	var errResp *gh.ErrorResponse
	if errors.As(err, &errResp) {
		if errResp.Response.StatusCode == http.StatusUnauthorized || errResp.Response.StatusCode == http.StatusForbidden {
			_ = h.DB.ClearUserToken(context.Background(), ctx.EffectiveUser.Id)
			_, _ = ctx.CallbackQuery.Answer(b, &gotgbot.AnswerCallbackQueryOpts{Text: "GitHub auth error. Token revoked or expired.", ShowAlert: true})
			return true
		}
	}
	return false
}
