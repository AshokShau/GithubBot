package middleware

import (
	"context"

	"github-webhook/internal/db"
	"github-webhook/internal/models"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/PaulSonOfLars/gotgbot/v2/ext"
)

func TrackUserAndChat(database *db.DB) func(b *gotgbot.Bot, ctx *ext.Context) error {
	return func(b *gotgbot.Bot, ctx *ext.Context) error {
		if ctx.EffectiveChat != nil {
			chatType := ctx.EffectiveChat.Type
			dbChat := &models.Chat{
				ID:       ctx.EffectiveChat.Id,
				ChatType: chatType,
				Title:    ctx.EffectiveChat.Title,
			}
			if dbChat.Title == "" {
				dbChat.Title = ctx.EffectiveChat.Username
			}

			go func() {
				_ = database.UpsertChat(context.Background(), dbChat)
			}()
		}
		return nil
	}
}
