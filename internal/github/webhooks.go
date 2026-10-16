package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github-webhook/internal/cache"
	"github-webhook/internal/config"
	"github-webhook/internal/db"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	"github.com/google/go-github/v92/github"
)

type WebhookServer struct {
	deliveryMu      sync.Mutex
	deliveries      map[string]deliveryEntry
	lastUnsupported time.Time
	lastMalformed   time.Time
	Config          *config.Config
	DB              *db.DB
	Bot             *gotdbot.Client
	ClientFactory   *ClientFactory
	ContextCache    *cache.Cache[string, models.MessageContext]  // Key: "chat_id:message_id"
	ActionCache     *cache.Cache[string, models.PRActionContext] // Key: UUID
	AdminCache      *cache.Cache[int64, []int64]
}

func NewWebhookServer(cfg *config.Config, database *db.DB, bot *gotdbot.Client, factory *ClientFactory, ctxCache *cache.Cache[string, models.MessageContext], actionCache *cache.Cache[string, models.PRActionContext], adminCache *cache.Cache[int64, []int64]) *WebhookServer {
	return &WebhookServer{
		Config:        cfg,
		DB:            database,
		Bot:           bot,
		ClientFactory: factory,
		ContextCache:  ctxCache,
		ActionCache:   actionCache,
		AdminCache:    adminCache,
	}
}

// Handler validates before processing and acknowledges only completed delivery.
// Successful delivery IDs are remembered per destination for 24 hours.
func (s *WebhookServer) Handler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Use POST for webhook deliveries.", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/webhook/")
	decrypted, err := utils.Decrypt(token, s.Config.EncryptionKey)
	if err != nil {
		http.Error(w, "Invalid webhook token.", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(decrypted, ":")
	chatID, err := strconv.ParseInt(parts[0], 10, 64)
	var topicID int64
	if len(parts) > 2 || err != nil || chatID == 0 {
		http.Error(w, "Invalid webhook destination.", http.StatusUnauthorized)
		return
	}
	if len(parts) == 2 {
		topicID, err = strconv.ParseInt(parts[1], 10, 32)
		if err != nil || topicID < 0 {
			http.Error(w, "Invalid webhook topic.", http.StatusUnauthorized)
			return
		}
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || (mediaType != "application/json" && mediaType != "application/x-www-form-urlencoded") {
		http.Error(w, "Use JSON or form-encoded webhook payloads.", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "Webhook payload is too large.", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Could not read the webhook payload.", http.StatusBadRequest)
		}
		return
	}
	signature := r.Header.Get(github.SHA256SignatureHeader)
	if signature == "" {
		signature = r.Header.Get(github.SHA1SignatureHeader)
	}
	err = github.ValidateSignature(signature, body, []byte(s.Config.GitHubWebhookSecret))
	if err != nil {
		reason := "invalid_signature"
		recovery := "The webhook signature header is malformed. Check that your proxy forwards GitHub's original signing headers."
		switch err.Error() {
		case "missing signature":
			reason = "missing_signature"
			recovery = "No webhook signature was received. Set the GitHub webhook Secret to the running bot's GITHUB_WEBHOOK_SECRET and ensure your proxy forwards X-Hub-Signature-256."
		case "payload signature check failed":
			reason = "signature_mismatch"
			recovery = "Webhook signature mismatch. Set the GitHub webhook Secret to the running bot's GITHUB_WEBHOOK_SECRET and ensure your proxy preserves the original request body."
		}
		s.logMalformed(fmt.Sprintf("Webhook rejected reason=%s response_status=401 chat_id=%d topic_id=%d delivery_id=%q: %s", reason, chatID, topicID, utils.Clip(github.DeliveryID(r), 100), recovery), nil)
		http.Error(w, recovery, http.StatusUnauthorized)
		return
	}
	payloadBytes, err := github.ValidatePayloadFromBody(mediaType, bytes.NewReader(body), "", nil)
	if err != nil {
		s.logMalformed("Invalid webhook body encoding response_status=400", err)
		http.Error(w, "Invalid webhook body encoding. Use application/json or a form with a valid payload field.", http.StatusBadRequest)
		return
	}
	var raw map[string]json.RawMessage
	if jsonErr := json.Unmarshal(payloadBytes, &raw); jsonErr != nil || raw == nil {
		s.logMalformed("Decode webhook JSON", jsonErr)
		http.Error(w, "Expected a JSON webhook object.", http.StatusBadRequest)
		return
	}
	eventType := github.WebHookType(r)
	if eventType == "" {
		http.Error(w, "Missing webhook event type.", http.StatusBadRequest)
		return
	}
	if _, ok := eventSpecs[eventType]; !ok {
		s.logUnsupported(eventType)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	event, err := github.ParseWebHook(eventType, payloadBytes)
	if err != nil {
		s.logMalformed("Parse webhook "+eventType, err)
		http.Error(w, "Invalid webhook payload.", http.StatusBadRequest)
		return
	}
	var action string
	_ = json.Unmarshal(raw["action"], &action)
	if !knownAction(eventType, action) {
		s.logUnsupported(eventType + ":unsupported_action")
	}
	// Resource-bearing events need their primary object. Optional subfields may be absent.
	if resource := eventSpecs[eventType].resource; resource != "" {
		if v, ok := raw[resource]; !ok || string(v) == "null" {
			s.logMalformed("Missing webhook resource for "+eventType, nil)
			http.Error(w, "Missing webhook resource.", http.StatusBadRequest)
			return
		}
	}
	hookID, _ := strconv.ParseInt(r.Header.Get("X-GitHub-Hook-ID"), 10, 64)
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	deliveryLogger := s.Bot.Logger.With("event", eventType, "chat_id", chatID, "topic_id", topicID, "hook_id", hookID, "delivery_id", fmt.Sprintf("%q", utils.Clip(deliveryID, 100)))
	key := fmt.Sprintf("%d:%d:%d:%s", chatID, topicID, hookID, deliveryID)
	if deliveryID != "" {
		state := s.beginDelivery(key)
		if state == deliveryDone {
			deliveryLogger.Info("Webhook skipped: notification already sent", "result", "duplicate")
			w.Header().Set("X-Webhook-Result", "duplicate")
			_, _ = fmt.Fprintln(w, "Duplicate delivery: this notification was already sent.")
			return
		}
		if state == deliveryBusy {
			deliveryLogger.Info("Webhook already processing", "result", "busy")
			w.Header().Set("X-Webhook-Result", "busy")
			http.Error(w, "Delivery is already processing. Retry later.", http.StatusServiceUnavailable)
			return
		}
	}
	deliveryLogger.Info("Webhook processing started")
	sentMsg, err := s.processEvent(event, eventType, payloadBytes, chatID, int32(topicID), hookID)
	if deliveryID != "" {
		// Muted deliveries remain eligible for explicit redelivery after /unmute.
		s.finishDelivery(key, err == nil && sentMsg != nil)
	}
	if err != nil {
		deliveryLogger.Warnf("Webhook processing failed: %s", utils.ErrorDetails(err))
		w.Header().Set("X-Webhook-Result", "failed")
		http.Error(w, "Webhook processing failed. Redeliver this event after resolving the delivery problem.", http.StatusBadGateway)
		return
	}
	if sentMsg == nil {
		deliveryLogger.Info("Webhook skipped: notifications are muted; use /unmute in this chat/topic", "result", "muted")
		w.Header().Set("X-Webhook-Result", "muted")
		_, _ = fmt.Fprintf(w, "Notifications are muted for Telegram chat %d, topic %d. Use /unmute there to resume notifications.\n", chatID, topicID)
		return
	}
	deliveryLogger.Info("Webhook notification sent", "result", "sent", "message_id", sentMsg.Id, "telegram_chat_id", sentMsg.ChatId)
	w.Header().Set("X-Webhook-Result", "sent")
	_, _ = fmt.Fprintf(w, "Notification sent to Telegram chat %d, topic %d (message %d).\n", sentMsg.ChatId, topicID, sentMsg.Id)
}

const (
	deliveryNew = iota
	deliveryBusy
	deliveryDone
)

type deliveryEntry struct {
	done    bool
	expires time.Time
}

func (s *WebhookServer) beginDelivery(key string) int {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if s.deliveries == nil {
		s.deliveries = map[string]deliveryEntry{}
	}
	now := time.Now()
	for k, v := range s.deliveries {
		if now.After(v.expires) {
			delete(s.deliveries, k)
		}
	}
	if v, ok := s.deliveries[key]; ok {
		if v.done {
			return deliveryDone
		}
		return deliveryBusy
	}
	// Bound memory; eviction permits a retry to notify again under extreme load.
	if len(s.deliveries) >= 10000 {
		for k, v := range s.deliveries {
			if v.done {
				delete(s.deliveries, k)
				break
			}
		}
	}
	if len(s.deliveries) >= 10000 {
		return deliveryBusy
	}
	s.deliveries[key] = deliveryEntry{expires: now.Add(24 * time.Hour)}
	return deliveryNew
}
func (s *WebhookServer) finishDelivery(key string, success bool) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if !success {
		delete(s.deliveries, key)
		return
	}
	s.deliveries[key] = deliveryEntry{done: true, expires: time.Now().Add(24 * time.Hour)}
}
func (s *WebhookServer) logUnsupported(name string) {
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	if time.Since(s.lastUnsupported) < time.Minute {
		return
	}
	s.lastUnsupported = time.Now()
	// Header values are untrusted; quote and bound them to prevent log injection.
	s.Bot.Logger.Warnf("unsupported_webhook_event=%q", utils.Clip(name, 80))
}

func (s *WebhookServer) logMalformed(stage string, err error) {
	s.deliveryMu.Lock()
	if time.Since(s.lastMalformed) < time.Minute {
		s.deliveryMu.Unlock()
		return
	}
	s.lastMalformed = time.Now()
	s.deliveryMu.Unlock()
	if err != nil {
		stage += ": " + utils.ErrorDetails(err)
	}
	s.Bot.Logger.Warnf("%s", stage)
}

func (s *WebhookServer) processEvent(event any, eventType string, payloadBytes []byte, chatID int64, topicID int32, hookID int64) (*gotdbot.Message, error) {
	if s.DB != nil {
		chat, err := s.DB.GetChat(context.Background(), chatID)
		if err != nil {
			return nil, err
		}
		if chat != nil && slices.Contains(chat.MutedThreads, int64(topicID)) {
			return nil, nil
		}
	}
	if repositoryEvent, ok := event.(*github.RepositoryEvent); ok && repositoryEvent.GetAction() == "renamed" && hookID != 0 && s.DB != nil {
		newName := repositoryEvent.GetRepo().GetFullName()
		if newName != "" {
			if err := s.DB.UpdateRepoLinkName(context.Background(), chatID, hookID, newName); err != nil {
				return nil, err
			}
			s.Bot.Logger.Infof("repository_link_renamed chat_id=%d", chatID)
		}
	}
	msg, markup := formatEvent(eventType, payloadBytes)

	if msg == nil {
		return nil, fmt.Errorf("event has no formatter")
	}

	opts := &gotdbot.SendMessageOpts{}

	if topicID != 0 {
		opts.TopicId = &gotdbot.MessageTopicForum{ForumTopicId: topicID}
	}

	if markup != nil {
		opts.ReplyMarkup = markup
	}

	if s.Bot == nil {
		return nil, fmt.Errorf("Telegram client is unavailable")
	}
	sentMsg, err := s.Bot.SendRichMessage(chatID, msg, opts)
	if err != nil {
		return nil, err
	}
	if sentMsg == nil || sentMsg.Id == 0 {
		return nil, fmt.Errorf("Telegram did not return a sent message")
	}

	if s.ContextCache != nil {
		if ctx, ok := messageContext(event); ok {
			s.ContextCache.Set(fmt.Sprintf("%d:%d", chatID, sentMsg.Id), ctx, 48*time.Hour)
		}
	}
	return sentMsg, nil
}
