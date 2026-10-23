package main

//go:generate go run github.com/AshokShau/gotdbot/scripts/tools

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github-webhook/internal/bot/callbacks"
	"github-webhook/internal/bot/commands"
	"github-webhook/internal/bot/middleware"
	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	"github.com/AshokShau/gotdbot/logger"
)

func main() {
	appLogger := logger.New()
	cfg := config.Load(appLogger)
	database, err := db.Connect(cfg)
	if err != nil {
		appLogger.Errorf("Failed to connect to DB. Check the server configuration: %s", utils.ErrorDetails(err))
		os.Exit(1)
	}

	oauth := github.NewOAuth(cfg)
	clientFactory := github.NewClientFactory()
	oauthStateCache := cache.New[string, int64]()
	contextCache := cache.New[string, models.MessageContext]()
	actionCache := cache.New[string, models.PRActionContext]()
	adminCache := cache.New[int64, []int64]()
	reloadRateLimit := cache.New[int64, time.Time]()
	menuOwners := cache.New[[2]int64, int64]()

	// TODO: add cfg.ApiID or cfg.ApiHash

	bot, err := gotdbot.NewClient(6, "eb06d4abfb49dc3eeb1aeb98ae0f581e", cfg.TelegramToken, &gotdbot.ClientOpts{
		Logger:      appLogger,
		LibraryPath: "./libtdjson.so.1.8.69",
		AutoRetry:   &gotdbot.AutoRetry{MaxFloodWait: 10 * time.Second, ChatNotFound: true, MessageNotFound: true},
		ErrorHandler: func(c *gotdbot.Client, update gotdbot.TlObject, err error) error {
			c.Logger.Errorf("Bot handler failed update=%s chat_id=%d: %s", update.GetType(), c.UpdateChatID(update), utils.ErrorDetails(err))
			return nil
		},
	})

	if err != nil {
		appLogger.Errorf("Failed to create bot. Check the server configuration: %s", utils.ErrorDetails(err))
		os.Exit(1)
	}

	bot.OnUpdateNewChat(middleware.TrackChat(database), nil)
	//bot.OnUpdateUser(middleware.TrackUser(database), nil)

	// Register commands once so private-chat fallbacks can identify unknown names.
	registeredCommands := map[string]bool{}
	registerCommand := func(name string, handler func(*gotdbot.Client, *gotdbot.Message) error) {
		registeredCommands[name] = true
		bot.OnCommand(name, handler)
	}

	// Commands
	cmdHandler := commands.NewCommandHandler(cfg, database, oauth, oauthStateCache, clientFactory, cfg.EncryptionKey, contextCache, adminCache, reloadRateLimit, menuOwners)
	bot.OnUpdateNewGuestQuery(cmdHandler.Guest, nil)
	registerCommand("start", cmdHandler.Start)
	registerCommand("connect", cmdHandler.Connect)
	registerCommand("disconnect", cmdHandler.Logout)
	registerCommand("logout", cmdHandler.Logout)
	registerCommand("me", cmdHandler.Me)

	registerCommand("add", cmdHandler.AddRepo)
	registerCommand("addrepo", cmdHandler.AddRepo)
	registerCommand("rm", cmdHandler.RemoveRepo)
	registerCommand("removerepo", cmdHandler.RemoveRepo)
	registerCommand("repos", cmdHandler.Repos)
	registerCommand("repo", cmdHandler.Repo)
	registerCommand("star", cmdHandler.Star)
	registerCommand("unstar", cmdHandler.Unstar)
	registerCommand("watch", cmdHandler.Watch)
	registerCommand("unwatch", cmdHandler.Unwatch)
	registerCommand("fork", cmdHandler.Fork)
	registerCommand("archive", cmdHandler.Archive)
	registerCommand("unarchive", cmdHandler.Unarchive)
	registerCommand("contributors", cmdHandler.Contributors)
	registerCommand("languages", cmdHandler.Languages)
	registerCommand("branches", cmdHandler.Branches)
	registerCommand("branch", cmdHandler.Branch)
	registerCommand("default", cmdHandler.Default)

	registerCommand("issue", cmdHandler.Issue)
	registerCommand("comment", cmdHandler.Comment)
	registerCommand("close", cmdHandler.Close)
	registerCommand("reopen", cmdHandler.Reopen)
	registerCommand("assign", cmdHandler.Assign)
	registerCommand("assignme", cmdHandler.AssignMe)
	registerCommand("unassign", cmdHandler.Unassign)
	registerCommand("label", cmdHandler.Label)
	registerCommand("labels", cmdHandler.Labels)
	registerCommand("milestone", cmdHandler.Milestone)
	registerCommand("lock", cmdHandler.Lock)
	registerCommand("unlock", cmdHandler.Unlock)
	registerCommand("pin", cmdHandler.Pin)
	registerCommand("unpin", cmdHandler.Unpin)

	registerCommand("approve", cmdHandler.Approve)
	registerCommand("requestchanges", cmdHandler.RequestChanges)
	registerCommand("merge", cmdHandler.Merge)
	registerCommand("draft", cmdHandler.Draft)
	registerCommand("ready", cmdHandler.Ready)
	registerCommand("checks", cmdHandler.Checks)
	registerCommand("files", cmdHandler.Files)
	registerCommand("diff", cmdHandler.Diff)
	registerCommand("reviews", cmdHandler.Reviews)
	registerCommand("mergeable", cmdHandler.Mergeable)
	registerCommand("request", cmdHandler.RequestReview)

	registerCommand("commit", cmdHandler.Commit)
	registerCommand("commits", cmdHandler.Commits)
	registerCommand("compare", cmdHandler.Compare)

	registerCommand("actions", cmdHandler.Actions)
	registerCommand("run", cmdHandler.RunWorkflow)
	registerCommand("rerun", cmdHandler.RerunWorkflow)
	registerCommand("cancel", cmdHandler.CancelWorkflow)
	registerCommand("logs", cmdHandler.WorkflowLogs)

	registerCommand("release", cmdHandler.Release)
	registerCommand("changelog", cmdHandler.Changelog)

	registerCommand("discussion", cmdHandler.CreateDiscussion)
	registerCommand("answered", cmdHandler.Answered)

	registerCommand("find", cmdHandler.Find)
	registerCommand("pr", cmdHandler.PRSearch)
	registerCommand("search", cmdHandler.SearchCode)

	registerCommand("mute", cmdHandler.Mute)
	registerCommand("unmute", cmdHandler.Unmute)
	registerCommand("done", cmdHandler.Done)
	registerCommand("read", cmdHandler.Read)
	registerCommand("stats", cmdHandler.Stats)
	registerCommand("activity", cmdHandler.Activity)

	registerCommand("config", cmdHandler.Settings)
	registerCommand("settings", cmdHandler.Settings)
	registerCommand("reload", cmdHandler.Reload)
	registerCommand("privacy", cmdHandler.Privacy)
	registerCommand("help", cmdHandler.Help)

	replyHandler := commands.NewReplyHandler(database, clientFactory, cfg.EncryptionKey, contextCache)
	bot.OnUpdateNewMessage(func(c *gotdbot.Client, u *gotdbot.UpdateNewMessage) error {
		if u.Message != nil && u.Message.IsPrivate() && u.Message.IsCommand() {
			parts := strings.Fields(u.Message.GetText())
			if len(parts) > 0 {
				command, _, _ := strings.Cut(strings.TrimPrefix(parts[0], "/"), "@")
				if !registeredCommands[command] {
					_, err := u.Message.ReplyText(c, "Unknown command. Use /help to see the available commands.", nil)
					return err
				}
			}
		}
		if u.Message != nil && u.Message.ReplyToMessageID() != 0 && !u.Message.IsCommand() {
			return replyHandler.HandleReply(c, u.Message)
		}
		return nil
	}, nil)

	cbHandler := callbacks.NewCallbackHandler(cfg, database, clientFactory, cfg.EncryptionKey, actionCache, adminCache, menuOwners)
	bot.OnUpdateNewCallbackQuery(func(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
		data := u.DataString()
		if strings.HasPrefix(data, "c:") {
			return cbHandler.HandleSettings(c, u)
		} else if strings.HasPrefix(data, "act:") {
			return cbHandler.HandlePRAction(c, u)
		}
		return u.Answer(c, "This interaction is no longer supported. Open /settings again.", &gotdbot.CallbackAnswerOpts{ShowAlert: true})
	}, nil)

	err = bot.Start()
	if err != nil {
		appLogger.Errorf("Failed to start bot. Check the server configuration: %s", utils.ErrorDetails(err))
		os.Exit(1)
	}

	me, err := bot.GetMe()
	botUsername := ""
	if err == nil && me != nil && me.Usernames != nil && len(me.Usernames.ActiveUsernames) > 0 {
		botUsername = me.Usernames.ActiveUsernames[0]
	}

	http.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		html := fmt.Sprintf(`
		<html>
		<head><title>GitHub Webhook Bot</title></head>
		<body style="font-family: sans-serif; text-align: center; padding: 50px;">
			<h1>GitHub Webhook Bot</h1>
			<p>The bot is running successfully.</p>
			<p><a href="https://t.me/%s" style="text-decoration: none; background-color: #0088cc; color: white; padding: 10px 20px; border-radius: 5px;">Open in Telegram</a></p>
		</body>
		</html>`, botUsername)
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(html))
	})

	webhookHandler := github.NewWebhookServer(cfg, database, bot, clientFactory, contextCache, actionCache, adminCache).Handler
	http.HandleFunc("/webhook/", webhookHandler)
	http.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")

		if code == "" {
			http.Error(w, "GitHub did not provide an authorization code. Return to Telegram and use /connect again.", http.StatusBadRequest)
			return
		}

		telegramID, ok := oauthStateCache.Get(state)
		if !ok {
			http.Error(w, "This connection link has expired or is invalid. Return to Telegram and use /connect again.", http.StatusBadRequest)
			return
		}

		oauthStateCache.Delete(state)
		token, err := oauth.ExchangeCode(context.Background(), code)
		if err != nil {
			http.Error(w, "GitHub authorization failed. Return to Telegram and use /connect again.", http.StatusInternalServerError)
			return
		}

		encToken, err := utils.Encrypt(token.AccessToken, cfg.EncryptionKey)
		if err != nil {
			http.Error(w, "Could not save the GitHub connection. Please try /connect again.", http.StatusInternalServerError)
			return
		}

		ghClient, err := clientFactory.GetUserClient(context.Background(), token.AccessToken)
		if err != nil {
			http.Error(w, "GitHub is unavailable. Please try /connect again later.", http.StatusInternalServerError)
			return
		}
		u, _, err := ghClient.Users.Get(context.Background(), "")
		if err != nil {
			http.Error(w, "Could not verify your GitHub account. Please try /connect again.", http.StatusInternalServerError)
			return
		}

		user := &models.User{
			ID:                  telegramID,
			GitHubUserID:        u.GetID(),
			GitHubUsername:      u.GetLogin(),
			EncryptedOAuthToken: encToken,
		}

		if err = database.UpsertUser(context.Background(), user); err != nil {
			http.Error(w, "Could not save the GitHub connection. Please try /connect again later.", http.StatusInternalServerError)
			return
		}

		_, _ = bot.SendMessage(telegramID, "<b>GitHub connected</b>\n\nAccount: "+utils.Code(u.GetLogin())+"\nUse /addrepo to choose a repository.", &gotdbot.SendMessageOpts{ParseMode: gotdbot.ParseModeHTML})

		html := fmt.Sprintf(`
		<html>
		<head><title>Connected</title></head>
		<body style="font-family: sans-serif; text-align: center; padding: 50px;">
			<h1>Authentication Successful</h1>
			<p>Your GitHub account has been connected.</p>
			<script>
				window.opener = null;
				setTimeout(function() { window.close(); }, 1000);
				setTimeout(function() { window.location.href = "https://t.me/%s"; }, 2000);
			</script>
			<p>If the window does not close automatically, you can <a href="https://t.me/%s">return to Telegram</a>.</p>
		</body>
		</html>`, botUsername, botUsername)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	})

	bot.Logger.Infof("Server listening on port %s", cfg.Port)
	if err = http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		appLogger.Errorf("Server failed. Check the server configuration: %s", utils.ErrorDetails(err))
		os.Exit(1)
	}
}
