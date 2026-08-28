package main

//go:generate go run github.com/AshokShau/gotdbot/scripts/tools

import (
	"context"
	"fmt"
	"log"
	"net/http"
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

	// TODO: add cfg.ApiID or cfg.ApiHash

	bot, err := gotdbot.NewClient(6, "eb06d4abfb49dc3eeb1aeb98ae0f581e", cfg.TelegramToken, &gotdbot.ClientOpts{
		LibraryPath: "./libtdjson.so.1.8.67",
		AutoRetry:   &gotdbot.AutoRetry{MaxFloodWait: 8 * time.Minute, ChatNotFound: true},
	})

	if err != nil {
		log.Fatalf("Failed to create bot: %v", err)
	}

	bot.OnUpdateNewChat(middleware.TrackChat(database), nil)
	//bot.OnUpdateUser(middleware.TrackUser(database), nil)

	// Commands
	cmdHandler := commands.NewCommandHandler(cfg, database, oauth, oauthStateCache, clientFactory, cfg.EncryptionKey, contextCache, adminCache, reloadRateLimit)
	bot.OnCommand("start", cmdHandler.Start)
	bot.OnCommand("connect", cmdHandler.Connect)
	bot.OnCommand("disconnect", cmdHandler.Logout)
	bot.OnCommand("logout", cmdHandler.Logout)
	bot.OnCommand("me", cmdHandler.Me)

	bot.OnCommand("add", cmdHandler.AddRepo)
	bot.OnCommand("addrepo", cmdHandler.AddRepo)
	bot.OnCommand("rm", cmdHandler.RemoveRepo)
	bot.OnCommand("removerepo", cmdHandler.RemoveRepo)
	bot.OnCommand("repos", cmdHandler.Repos)
	bot.OnCommand("repo", cmdHandler.Repo)
	bot.OnCommand("star", cmdHandler.Star)
	bot.OnCommand("unstar", cmdHandler.Unstar)
	bot.OnCommand("watch", cmdHandler.Watch)
	bot.OnCommand("unwatch", cmdHandler.Unwatch)
	bot.OnCommand("fork", cmdHandler.Fork)
	bot.OnCommand("archive", cmdHandler.Archive)
	bot.OnCommand("unarchive", cmdHandler.Unarchive)
	bot.OnCommand("contributors", cmdHandler.Contributors)
	bot.OnCommand("languages", cmdHandler.Languages)
	bot.OnCommand("branches", cmdHandler.Branches)
	bot.OnCommand("branch", cmdHandler.Branch)
	bot.OnCommand("default", cmdHandler.Default)

	bot.OnCommand("issue", cmdHandler.Issue)
	bot.OnCommand("comment", cmdHandler.Comment)
	bot.OnCommand("close", cmdHandler.Close)
	bot.OnCommand("reopen", cmdHandler.Reopen)
	bot.OnCommand("assign", cmdHandler.Assign)
	bot.OnCommand("assignme", cmdHandler.AssignMe)
	bot.OnCommand("unassign", cmdHandler.Unassign)
	bot.OnCommand("label", cmdHandler.Label)
	bot.OnCommand("labels", cmdHandler.Labels)
	bot.OnCommand("milestone", cmdHandler.Milestone)
	bot.OnCommand("lock", cmdHandler.Lock)
	bot.OnCommand("unlock", cmdHandler.Unlock)
	bot.OnCommand("pin", cmdHandler.Pin)
	bot.OnCommand("unpin", cmdHandler.Unpin)

	bot.OnCommand("approve", cmdHandler.Approve)
	bot.OnCommand("requestchanges", cmdHandler.RequestChanges)
	bot.OnCommand("merge", cmdHandler.Merge)
	bot.OnCommand("draft", cmdHandler.Draft)
	bot.OnCommand("ready", cmdHandler.Ready)
	bot.OnCommand("checks", cmdHandler.Checks)
	bot.OnCommand("files", cmdHandler.Files)
	bot.OnCommand("diff", cmdHandler.Diff)
	bot.OnCommand("reviews", cmdHandler.Reviews)
	bot.OnCommand("mergeable", cmdHandler.Mergeable)
	bot.OnCommand("request", cmdHandler.RequestReview)

	bot.OnCommand("commit", cmdHandler.Commit)
	bot.OnCommand("commits", cmdHandler.Commits)
	bot.OnCommand("compare", cmdHandler.Compare)

	bot.OnCommand("actions", cmdHandler.Actions)
	bot.OnCommand("run", cmdHandler.RunWorkflow)
	bot.OnCommand("rerun", cmdHandler.RerunWorkflow)
	bot.OnCommand("cancel", cmdHandler.CancelWorkflow)
	bot.OnCommand("logs", cmdHandler.WorkflowLogs)

	bot.OnCommand("release", cmdHandler.Release)
	bot.OnCommand("changelog", cmdHandler.Changelog)

	bot.OnCommand("discussion", cmdHandler.CreateDiscussion)
	bot.OnCommand("answered", cmdHandler.Answered)

	bot.OnCommand("find", cmdHandler.Find)
	bot.OnCommand("pr", cmdHandler.PRSearch)
	bot.OnCommand("search", cmdHandler.SearchCode)

	bot.OnCommand("mute", cmdHandler.Mute)
	bot.OnCommand("done", cmdHandler.Done)
	bot.OnCommand("read", cmdHandler.Read)
	bot.OnCommand("stats", cmdHandler.Stats)
	bot.OnCommand("activity", cmdHandler.Activity)

	bot.OnCommand("config", cmdHandler.Settings)
	bot.OnCommand("settings", cmdHandler.Settings)
	bot.OnCommand("reload", cmdHandler.Reload)
	bot.OnCommand("privacy", cmdHandler.Privacy)
	bot.OnCommand("help", cmdHandler.Help)

	replyHandler := commands.NewReplyHandler(database, clientFactory, cfg.EncryptionKey, contextCache)
	bot.OnUpdateNewMessage(func(c *gotdbot.Client, u *gotdbot.UpdateNewMessage) error {
		if u.Message != nil && u.Message.ReplyToMessageID() != 0 && !u.Message.IsCommand() {
			return replyHandler.HandleReply(c, u.Message)
		}
		return nil
	}, nil)

	cbHandler := callbacks.NewCallbackHandler(cfg, database, clientFactory, cfg.EncryptionKey, actionCache, adminCache)
	bot.OnUpdateNewCallbackQuery(func(c *gotdbot.Client, u *gotdbot.UpdateNewCallbackQuery) error {
		data := u.DataString()
		if strings.HasPrefix(data, "c:") {
			return cbHandler.HandleSettings(c, u)
		} else if strings.HasPrefix(data, "act:") {
			return cbHandler.HandlePRAction(c, u)
		}
		return nil
	}, nil)

	err = bot.Start()
	if err != nil {
		log.Fatalf("Failed to start bot: %v", err)
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

		if err = database.UpsertUser(context.Background(), user); err != nil {
			http.Error(w, "DB Error", http.StatusInternalServerError)
			return
		}

		_, _ = bot.SendTextMessage(telegramID, fmt.Sprintf("✅ GitHub account <b>%s</b> connected successfully!", u.GetLogin()), &gotdbot.SendTextMessageOpts{ParseMode: gotdbot.ParseModeHTML})

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

	log.Printf("Server listening on port %s", cfg.Port)
	if err = http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
