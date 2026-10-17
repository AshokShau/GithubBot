package callbacks

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github-webhook/internal/bot/settings"
	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v92/github"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type CallbackHandler struct {
	Config        *config.Config
	DB            *db.DB
	ClientFactory *github.ClientFactory
	EncryptionKey string
	ActionCache   *cache.Cache[string, models.PRActionContext]
	AdminCache    *cache.Cache[int64, []int64]
	MenuOwners    *cache.Cache[[2]int64, int64]
}

func NewCallbackHandler(cfg *config.Config, database *db.DB, factory *github.ClientFactory, key string, actionCache *cache.Cache[string, models.PRActionContext], adminCache *cache.Cache[int64, []int64], menuOwners *cache.Cache[[2]int64, int64]) *CallbackHandler {
	return &CallbackHandler{
		Config:        cfg,
		DB:            database,
		ClientFactory: factory,
		EncryptionKey: key,
		ActionCache:   actionCache,
		AdminCache:    adminCache,
		MenuOwners:    menuOwners,
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
	reopen := "/settings or /addrepo"
	if strings.HasPrefix(u.DataString(), "c:rl:") {
		reopen = "/repos"
	}
	if !validSettingsData(u.DataString()) {
		return u.Answer(c, "This interaction is invalid or expired. Open "+reopen+" again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	key := [2]int64{u.ChatId, u.MessageId}
	owner, found := h.MenuOwners.Get(key)
	if !found {
		return u.Answer(c, "This menu has expired. Open "+reopen+" again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	if owner != u.SenderUserId {
		return u.Answer(c, "This menu belongs to another user. Open "+reopen+" to use your own menu.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	if strings.HasPrefix(u.DataString(), "c:rl:") {
		links, err := h.DB.GetChatLinks(context.Background(), u.ChatId)
		if err != nil {
			c.Logger.Warnf("Could not load linked repositories: %s", utils.ErrorDetails(err))
			return u.Answer(c, "Could not load linked repositories. Try /repos again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		}
		page, _ := strconv.Atoi(strings.TrimPrefix(u.DataString(), "c:rl:"))
		h.MenuOwners.Set(key, owner, 30*time.Minute)
		_ = u.Answer(c, "", nil)
		_, err = u.EditRichMessageHTML(c, settings.LinkedRepositoryPage(links, page), &gotdbot.EditRichTextOpts{})
		return err
	}
	if !u.IsPrivate() && !utils.IsAdmin(c, u.ChatId, u.SenderUserId, h.AdminCache) {
		_ = u.Answer(c, "Telegram administrator access is required to change settings.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}
	h.MenuOwners.Set(key, owner, 30*time.Minute)

	data := u.DataString()
	parts := strings.Split(data, ":")

	if len(parts) < 2 {
		return nil
	}

	prefix := parts[0] // c, conf
	action := parts[1] // ls, r, te, ep

	if prefix == "c" {
		if action == "ls" {
			page := 1
			if len(parts) == 3 {
				page, _ = strconv.Atoi(parts[2])
			}
			return h.showRepoList(c, u, page)
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
				page := 1
				if len(parts) == 5 {
					page, _ = strconv.Atoi(parts[4])
				}
				return h.handleAddRepoByID(c, u, repoID, page)
			}
		}

		if len(parts) < 3 {
			return nil
		}

		repoName := parts[2]
		links, err := h.DB.GetChatLinks(context.Background(), u.ChatId)
		var link *models.RepoLink
		for i := range links {
			if links[i].RepoFullName == repoName || links[i].WebhookID > 0 && fmt.Sprint(links[i].WebhookID) == repoName {
				link = &links[i]
				break
			}
		}
		if err != nil || link == nil {
			_ = u.Answer(c, "Repository link unavailable. Open /settings again or use /addrepo.", &gotdbot.CallbackAnswerOpts{ShowAlert: false})
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
			if uErr != nil || user == nil || user.EncryptedOAuthToken == "" {
				if uErr != nil && !errors.Is(uErr, mongo.ErrNoDocuments) {
					c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(uErr))
				}
				_ = u.Answer(c, utils.ConnectionError(uErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
				return nil
			}
			token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
			if tErr != nil {
				_ = u.Answer(c, "GitHub authorization is unavailable. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
				return nil
			}

			ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
			if err != nil {
				_ = u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
				c.Logger.Warnf("Failed to fetch GitHub settings: %s", utils.ErrorDetails(hErr))
				_ = u.Answer(c, utils.OperationError("Failed to fetch GitHub settings", hErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
			if hook.Config == nil {
				hook.Config = &gh.HookConfig{}
			}
			hook.Config.Secret = new(h.Config.GitHubWebhookSecret)
			_, _, editErr := ghClient.Repositories.EditHook(context.Background(), owner, repoName, link.WebhookID, hook)
			if editErr != nil {
				if h.handleAuthError(c, u, editErr) {
					return nil
				}
				c.Logger.Warnf("Failed to update GitHub: %s", utils.ErrorDetails(editErr))
				_ = u.Answer(c, utils.OperationError("Failed to update GitHub", editErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
	repo := html.EscapeString(l.RepoFullName)
	if l.WebhookID > 0 {
		repo = fmt.Sprint(l.WebhookID)
	}
	text := "<h3>Notification settings</h3><p>" + utils.Code(l.RepoFullName) + "</p><p>Choose a preset or select individual events.</p>" +
		fmt.Sprintf(`<tg-button-row align="left"><tg-button type="callback_data" data="c:presets:%s:push">Push only</tg-button><tg-button type="callback_data" data="c:presets:%s:all">All events</tg-button></tg-button-row><tg-button-row align="left"><tg-button type="callback_data" data="c:iev:%s:1">Choose events</tg-button><tg-button type="callback_data" data="c:ls">Repositories</tg-button></tg-button-row>`, repo, repo, repo)
	_ = u.Answer(c, "", nil)
	_, err := u.EditRichMessageHTML(c, text, &gotdbot.EditRichTextOpts{})
	return err
}

func (h *CallbackHandler) handlePresets(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, l *models.RepoLink, mode string) error {
	user, uErr := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if uErr != nil || user == nil || user.EncryptedOAuthToken == "" {
		if uErr != nil && !errors.Is(uErr, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(uErr))
		}
		_ = u.Answer(c, utils.ConnectionError(uErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	token, tErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if tErr != nil {
		_ = u.Answer(c, "GitHub authorization is unavailable. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
		c.Logger.Warnf("Failed to fetch GitHub hook: %s", utils.ErrorDetails(hErr))
		_ = u.Answer(c, utils.OperationError("Failed to fetch GitHub hook", hErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
	if hook.Config == nil {
		hook.Config = &gh.HookConfig{}
	}
	hook.Config.Secret = new(h.Config.GitHubWebhookSecret)
	_, _, editErr := ghClient.Repositories.EditHook(context.Background(), owner, repoName, l.WebhookID, hook)
	if editErr != nil {
		if h.handleAuthError(c, u, editErr) {
			return nil
		}
		c.Logger.Warnf("Failed to update GitHub hook: %s", utils.ErrorDetails(editErr))
		_ = u.Answer(c, utils.OperationError("Failed to update GitHub hook", editErr), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	responseText := "<h3>Notifications updated</h3><p>" + utils.Code(l.RepoFullName) + "</p><p>All GitHub events. Unsupported events are skipped.</p>"
	if mode == "push" {
		responseText = "<h3>Notifications updated</h3><p>" + utils.Code(l.RepoFullName) + "</p><p>Push events only.</p>"
	}

	key := l.RepoFullName
	if l.WebhookID > 0 {
		key = fmt.Sprint(l.WebhookID)
	}
	responseText += fmt.Sprintf(`<tg-button-row align="left"><tg-button type="callback_data" data="%s">Back</tg-button></tg-button-row>`, html.EscapeString("c:r:"+key))
	_ = u.Answer(c, "", nil)
	_, err = u.EditRichMessageHTML(c, responseText, &gotdbot.EditRichTextOpts{})
	return err
}

func (h *CallbackHandler) showIndividualEvents(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, l *models.RepoLink, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		return u.Answer(c, utils.ConnectionError(err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		return u.Answer(c, "GitHub authorization is unavailable. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		return u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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
		c.Logger.Warnf("Could not fetch webhook settings: %s", utils.ErrorDetails(err))
		return u.Answer(c, utils.OperationError("Could not fetch webhook settings", err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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

	_ = u.Answer(c, "", nil)
	_, err = u.EditRichMessageHTML(c, settings.EventsPage(l.RepoFullName, l.WebhookID, enabledEvents, page), &gotdbot.EditRichTextOpts{})
	return err
}

func (h *CallbackHandler) showRepoList(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, page int) error {
	links, err := h.DB.GetChatLinks(context.Background(), u.ChatId)
	if err != nil {
		c.Logger.Warnf("Could not load linked repositories: %s", utils.ErrorDetails(err))
		return u.Answer(c, utils.OperationError("Could not load linked repositories", err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	_ = u.Answer(c, "", nil)
	_, err = u.EditRichMessageHTML(c, settings.RepositoryPage(links, page), &gotdbot.EditRichTextOpts{})
	return err
}

func (h *CallbackHandler) handleRepoPage(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		_ = u.Answer(c, utils.ConnectionError(err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_ = u.Answer(c, "GitHub authorization is unavailable. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}
	opts := &gh.RepositoryListByAuthenticatedUserOptions{
		Sort:      "updated",
		Direction: "desc",
		PerPage:   5, Page: page,
	}

	repos, resp, err := ghClient.Repositories.ListByAuthenticatedUser(context.Background(), opts)
	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		c.Logger.Warnf("GitHub API error: %s", utils.ErrorDetails(err))
		_ = u.Answer(c, utils.OperationError("GitHub API error", err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	_ = u.Answer(c, "", nil)
	_, err = u.EditRichMessageHTML(c, settings.DiscoveryPage(repos, resp, page), &gotdbot.EditRichTextOpts{})

	return err
}

func (h *CallbackHandler) handleAddRepoByID(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, repoID int64, page int) error {
	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		return u.Answer(c, utils.ConnectionError(err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	token, decErr := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if decErr != nil {
		return u.Answer(c, "GitHub authorization is unavailable. Use /connect again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}
	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	repo, _, err := ghClient.Repositories.GetByID(context.Background(), repoID)
	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		_ = u.Answer(c, "Repository unavailable. Check your GitHub access and open /addrepo again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	if existingLink, _ := h.DB.GetRepoLink(context.Background(), u.ChatId, repo.GetFullName()); existingLink != nil {
		_ = u.Answer(c, "", nil)
		_, err = u.EditRichMessageHTML(c, settings.RepositoryLinked(repo.GetFullName(), existingLink.WebhookID, page, true), &gotdbot.EditRichTextOpts{})
		return err
	}

	chatToken, encErr := utils.Encrypt(fmt.Sprintf("%d", u.ChatId), h.EncryptionKey)
	if encErr != nil {
		_ = u.Answer(c, "Could not prepare the webhook. Try again or contact the bot administrator.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
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

		_ = u.Answer(c, "", nil)
		c.Logger.Warnf("Webhook creation failed: %s", utils.ErrorDetails(hookErr))
		_, err = u.EditMessageText(c, utils.OperationError("Webhook creation failed", hookErr), &gotdbot.EditMessageTextOpts{
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
		c.Logger.Warnf("Save repository link: %s", utils.ErrorDetails(err))
		if _, cleanupErr := ghClient.Repositories.DeleteHook(context.Background(), repo.GetOwner().GetLogin(), repo.GetName(), webhookID); cleanupErr != nil {
			c.Logger.Warnf("Roll back repository webhook: %s", utils.ErrorDetails(cleanupErr))
			return u.Answer(c, "The webhook was created, but the link could not be saved. Remove the webhook in GitHub settings before trying /addrepo again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		}
		_ = u.Answer(c, "Could not save the repository link. Try again or contact the bot administrator.", &gotdbot.CallbackAnswerOpts{ShowAlert: false})
		return nil
	}

	_ = u.Answer(c, "", nil)
	_, err = u.EditRichMessageHTML(c, settings.RepositoryLinked(repo.GetFullName(), webhookID, page, false), &gotdbot.EditRichTextOpts{})
	return err
}

func (h *CallbackHandler) HandlePRAction(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
	data := u.DataString()
	parts := strings.Split(data, ":") // act:approve:uuid

	if len(parts) != 3 || parts[0] != "act" || (parts[1] != "approve" && parts[1] != "close") || parts[2] == "" {
		return u.Answer(c, "Unsupported action. Open the pull request on GitHub.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}

	action := parts[1]
	actionID := parts[2]

	prContext, ok := h.ActionCache.Get(actionID)
	if !ok {
		_ = u.Answer(c, "Action expired. Please open the PR link manually.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	owner := prContext.Owner
	repo := prContext.Repo
	prNum := prContext.PRNumber

	repoFullName := fmt.Sprintf("%s/%s", owner, repo)
	_, err := h.DB.GetRepoLink(context.Background(), u.ChatId, repoFullName)
	if err != nil {
		_ = u.Answer(c, "Repository not linked to this chat. Use /addrepo to link it.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	user, err := h.DB.GetUserByTelegramID(context.Background(), u.SenderUserId)
	if err != nil || user == nil || user.EncryptedOAuthToken == "" {
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			c.Logger.Warnf("Load GitHub connection: %s", utils.ErrorDetails(err))
		}
		_ = u.Answer(c, utils.ConnectionError(err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	token, err := utils.Decrypt(user.EncryptedOAuthToken, h.EncryptionKey)
	if err != nil {
		_ = u.Answer(c, "GitHub authorization is unavailable. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	ghClient, err := h.ClientFactory.GetUserClient(context.Background(), token)
	if err != nil {
		_ = u.Answer(c, "GitHub is unavailable. Please try again later.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}
	ctxBg := context.Background()

	var msg string

	switch action {
	case "approve":
		_, _, err = ghClient.PullRequests.CreateReview(ctxBg, owner, repo, prNum, &gh.PullRequestReviewRequest{Event: new("APPROVE")})
		msg = fmt.Sprintf("Approved %s/%s #%d.", owner, repo, prNum)
	case "close":
		_, _, err = ghClient.PullRequests.Edit(ctxBg, owner, repo, prNum, &gh.PullRequest{State: new("closed")})
		msg = fmt.Sprintf("Closed %s/%s #%d.", owner, repo, prNum)
	}

	if err != nil {
		if h.handleAuthError(c, u, err) {
			return nil
		}
		c.Logger.Warnf("Could not update the pull request: %s", utils.ErrorDetails(err))
		_ = u.Answer(c, utils.OperationError("Could not update the pull request", err), &gotdbot.CallbackAnswerOpts{ShowAlert: true})
		return nil
	}

	_ = u.Answer(c, msg, &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	return nil
}

func (h *CallbackHandler) handleAuthError(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery, err error) bool {
	if errResp, ok := errors.AsType[*gh.ErrorResponse](err); ok {
		if errResp.Response != nil && errResp.Response.StatusCode == http.StatusUnauthorized {
			_ = h.DB.ClearUserToken(context.Background(), u.SenderUserId)
			_ = u.Answer(c, "GitHub authorization has expired. Use /connect to reconnect.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
			return true
		}
	}
	return false
}
