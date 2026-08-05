package main

import (
	"context"
	"fmt"
	"github-webhook/internal/bot/middleware"
	"log"
	"net/http"
	"time"

	"github-webhook/internal/bot/callbacks"
	"github-webhook/internal/bot/commands"
	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers"
	"github.com/PaulSonOfLars/gotgbot/v2/ext/handlers/filters/callbackquery"
)

func main() {
	cfg := config.Load()
	database, err := db.Connect(cfg)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %v", err)
	}

	oauth := github.NewOAuth(cfg)
	clientFactory := github.NewClientFactory()
	oauthStateCache := cache.New[string, int64]()
	contextCache := cache.New[string, models.MessageContext]()
	actionCache := cache.New[string, models.PRActionContext]()
	adminCache := cache.New[int64, []int64]()
	reloadRateLimit := cache.New[int64, time.Time]()

	b, err := gotgbot.NewBot(cfg.TelegramToken, nil)
	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	dispatcher := ext.NewDispatcher(&ext.DispatcherOpts{
		Error: func(b *gotgbot.Bot, ctx *ext.Context, err error) ext.DispatcherAction {
			log.Printf("Error processing update: %v", err)
			return ext.DispatcherActionNoop
		},
	})
	updater := ext.NewUpdater(dispatcher, nil)
	dispatcher.AddHandlerToGroup(handlers.NewMessage(nil, middleware.TrackUserAndChat(database)), -1)

	// Commands
	cmdHandler := commands.NewCommandHandler(cfg, database, oauth, oauthStateCache, clientFactory, cfg.EncryptionKey, contextCache, adminCache, reloadRateLimit)
	dispatcher.AddHandler(handlers.NewCommand("start", cmdHandler.Start))
	dispatcher.AddHandler(handlers.NewCommand("connect", cmdHandler.Connect))
	dispatcher.AddHandler(handlers.NewCommand("disconnect", cmdHandler.Logout))
	dispatcher.AddHandler(handlers.NewCommand("logout", cmdHandler.Logout))
	dispatcher.AddHandler(handlers.NewCommand("me", cmdHandler.Me))

	dispatcher.AddHandler(handlers.NewCommand("add", cmdHandler.AddRepo))
	dispatcher.AddHandler(handlers.NewCommand("addrepo", cmdHandler.AddRepo))
	dispatcher.AddHandler(handlers.NewCommand("rm", cmdHandler.RemoveRepo))
	dispatcher.AddHandler(handlers.NewCommand("removerepo", cmdHandler.RemoveRepo))
	dispatcher.AddHandler(handlers.NewCommand("repos", cmdHandler.Repos))
	dispatcher.AddHandler(handlers.NewCommand("repo", cmdHandler.Repo))
	dispatcher.AddHandler(handlers.NewCommand("star", cmdHandler.Star))
	dispatcher.AddHandler(handlers.NewCommand("unstar", cmdHandler.Unstar))
	dispatcher.AddHandler(handlers.NewCommand("watch", cmdHandler.Watch))
	dispatcher.AddHandler(handlers.NewCommand("unwatch", cmdHandler.Unwatch))
	dispatcher.AddHandler(handlers.NewCommand("fork", cmdHandler.Fork))
	dispatcher.AddHandler(handlers.NewCommand("archive", cmdHandler.Archive))
	dispatcher.AddHandler(handlers.NewCommand("unarchive", cmdHandler.Unarchive))
	dispatcher.AddHandler(handlers.NewCommand("contributors", cmdHandler.Contributors))
	dispatcher.AddHandler(handlers.NewCommand("languages", cmdHandler.Languages))
	dispatcher.AddHandler(handlers.NewCommand("branches", cmdHandler.Branches))
	dispatcher.AddHandler(handlers.NewCommand("branch", cmdHandler.Branch))
	dispatcher.AddHandler(handlers.NewCommand("default", cmdHandler.Default))

	dispatcher.AddHandler(handlers.NewCommand("issue", cmdHandler.Issue))
	dispatcher.AddHandler(handlers.NewCommand("comment", cmdHandler.Comment))
	dispatcher.AddHandler(handlers.NewCommand("close", cmdHandler.Close))
	dispatcher.AddHandler(handlers.NewCommand("reopen", cmdHandler.Reopen))
	dispatcher.AddHandler(handlers.NewCommand("assign", cmdHandler.Assign))
	dispatcher.AddHandler(handlers.NewCommand("assignme", cmdHandler.AssignMe))
	dispatcher.AddHandler(handlers.NewCommand("unassign", cmdHandler.Unassign))
	dispatcher.AddHandler(handlers.NewCommand("label", cmdHandler.Label))
	dispatcher.AddHandler(handlers.NewCommand("labels", cmdHandler.Labels))
	dispatcher.AddHandler(handlers.NewCommand("milestone", cmdHandler.Milestone))
	dispatcher.AddHandler(handlers.NewCommand("lock", cmdHandler.Lock))
	dispatcher.AddHandler(handlers.NewCommand("unlock", cmdHandler.Unlock))
	dispatcher.AddHandler(handlers.NewCommand("pin", cmdHandler.Pin))
	dispatcher.AddHandler(handlers.NewCommand("unpin", cmdHandler.Unpin))

	dispatcher.AddHandler(handlers.NewCommand("approve", cmdHandler.Approve))
	dispatcher.AddHandler(handlers.NewCommand("requestchanges", cmdHandler.RequestChanges))
	dispatcher.AddHandler(handlers.NewCommand("merge", cmdHandler.Merge))
	dispatcher.AddHandler(handlers.NewCommand("draft", cmdHandler.Draft))
	dispatcher.AddHandler(handlers.NewCommand("ready", cmdHandler.Ready))
	dispatcher.AddHandler(handlers.NewCommand("checks", cmdHandler.Checks))
	dispatcher.AddHandler(handlers.NewCommand("files", cmdHandler.Files))
	dispatcher.AddHandler(handlers.NewCommand("diff", cmdHandler.Diff))
	dispatcher.AddHandler(handlers.NewCommand("reviews", cmdHandler.Reviews))
	dispatcher.AddHandler(handlers.NewCommand("mergeable", cmdHandler.Mergeable))
	dispatcher.AddHandler(handlers.NewCommand("request", cmdHandler.RequestReview))

	dispatcher.AddHandler(handlers.NewCommand("commit", cmdHandler.Commit))
	dispatcher.AddHandler(handlers.NewCommand("commits", cmdHandler.Commits))
	dispatcher.AddHandler(handlers.NewCommand("compare", cmdHandler.Compare))

	dispatcher.AddHandler(handlers.NewCommand("actions", cmdHandler.Actions))
	dispatcher.AddHandler(handlers.NewCommand("run", cmdHandler.RunWorkflow))
	dispatcher.AddHandler(handlers.NewCommand("rerun", cmdHandler.RerunWorkflow))
	dispatcher.AddHandler(handlers.NewCommand("cancel", cmdHandler.CancelWorkflow))
	dispatcher.AddHandler(handlers.NewCommand("logs", cmdHandler.WorkflowLogs))

	dispatcher.AddHandler(handlers.NewCommand("release", cmdHandler.Release))
	dispatcher.AddHandler(handlers.NewCommand("changelog", cmdHandler.Changelog))

	dispatcher.AddHandler(handlers.NewCommand("discussion", cmdHandler.CreateDiscussion))
	dispatcher.AddHandler(handlers.NewCommand("answered", cmdHandler.Answered))

	dispatcher.AddHandler(handlers.NewCommand("find", cmdHandler.Find))
	dispatcher.AddHandler(handlers.NewCommand("pr", cmdHandler.PRSearch))
	dispatcher.AddHandler(handlers.NewCommand("search", cmdHandler.SearchCode))

	dispatcher.AddHandler(handlers.NewCommand("mute", cmdHandler.Mute))
	dispatcher.AddHandler(handlers.NewCommand("done", cmdHandler.Done))
	dispatcher.AddHandler(handlers.NewCommand("read", cmdHandler.Read))
	dispatcher.AddHandler(handlers.NewCommand("stats", cmdHandler.Stats))
	dispatcher.AddHandler(handlers.NewCommand("activity", cmdHandler.Activity))

	dispatcher.AddHandler(handlers.NewCommand("config", cmdHandler.Settings))
	dispatcher.AddHandler(handlers.NewCommand("settings", cmdHandler.Settings))
	dispatcher.AddHandler(handlers.NewCommand("reload", cmdHandler.Reload))
	dispatcher.AddHandler(handlers.NewCommand("privacy", cmdHandler.Privacy))
	dispatcher.AddHandler(handlers.NewCommand("help", cmdHandler.Help))

	replyHandler := commands.NewReplyHandler(database, clientFactory, cfg.EncryptionKey, contextCache)
	dispatcher.AddHandler(handlers.NewMessage(func(msg *gotgbot.Message) bool {
		if msg.GetText() == "" {
			return false
		}

		ents := msg.GetEntities()
		if len(ents) != 0 && ents[0].Offset == 0 && ents[0].Type == "bot_command" {
			return false
		}

		return msg.ReplyToMessage != nil
	}, replyHandler.HandleReply))

	cbHandler := callbacks.NewCallbackHandler(cfg, database, clientFactory, cfg.EncryptionKey, actionCache, adminCache)
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("c:"), cbHandler.HandleSettings))
	dispatcher.AddHandler(handlers.NewCallback(callbackquery.Prefix("act:"), cbHandler.HandlePRAction))

	go func() {
		err = updater.StartPolling(b, &ext.PollingOpts{
			DropPendingUpdates: true,
			GetUpdatesOpts: &gotgbot.GetUpdatesOpts{
				Timeout: 9,
				RequestOpts: &gotgbot.RequestOpts{
					Timeout: time.Second * 10,
				},
			},
		})
		if err != nil {
			log.Fatalf("Failed to start polling: %v", err)
		}
	}()

	log.Printf("Bot started: @%s", b.User.Username)

	http.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		html := fmt.Sprintf(`
		<html>
		<head><title>GitHub Webhook Bot</title></head>
		<body style="font-family: sans-serif; text-align: center; padding: 50px;">
			<h1>GitHub Webhook Bot</h1>
			<p>The bot is running successfully.</p>
			<p><a href="https://t.me/%s" style="text-decoration: none; background-color: #0088cc; color: white; padding: 10px 20px; border-radius: 5px;">Open in Telegram</a></p>
		</body>
		</html>`, b.User.Username)
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(html))
	})

	webhookHandler := github.NewWebhookServer(cfg, database, b, contextCache, actionCache).Handler
	http.HandleFunc("/webhook/", webhookHandler)
	http.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")

		if code == "" {
			http.Error(w, "Missing code", http.StatusBadRequest)
			return
		}

		telegramID, ok := oauthStateCache.Get(state)
		if !ok {
			http.Error(w, "Invalid or expired state", http.StatusBadRequest)
			return
		}

		oauthStateCache.Delete(state)
		token, err := oauth.ExchangeCode(context.Background(), code)
		if err != nil {
			http.Error(w, "Failed to exchange code", http.StatusInternalServerError)
			return
		}

		encToken, err := utils.Encrypt(token.AccessToken, cfg.EncryptionKey)
		if err != nil {
			http.Error(w, "Encryption failed", http.StatusInternalServerError)
			return
		}

		ghClient, err := clientFactory.GetUserClient(context.Background(), token.AccessToken)
		if err != nil {
			http.Error(w, "Failed to create GitHub client", http.StatusInternalServerError)
			return
		}
		u, _, err := ghClient.Users.Get(context.Background(), "")
		if err != nil {
			http.Error(w, "Failed to fetch user", http.StatusInternalServerError)
			return
		}

		user := &models.User{
			ID:                  telegramID,
			GitHubUserID:        u.GetID(),
			GitHubUsername:      u.GetLogin(),
			EncryptedOAuthToken: encToken,
		}
		if err := database.UpsertUser(context.Background(), user); err != nil {
			http.Error(w, "DB Error", http.StatusInternalServerError)
			return
		}

		_, _ = b.SendMessage(telegramID, fmt.Sprintf("✅ GitHub account <b>%s</b> connected successfully!", u.GetLogin()), &gotgbot.SendMessageOpts{ParseMode: "HTML"})

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
		</html>`, b.User.Username, b.User.Username)
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(html))
	})

	log.Printf("Server listening on port %s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
