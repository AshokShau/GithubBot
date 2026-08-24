package middleware

import (
	"context"

	"github-webhook/internal/db"
	"github-webhook/internal/models"

	"github.com/AshokShau/gotdbot"
)

func TrackChat(database *db.DB) func(c *gotdbot.Client, u *gotdbot.UpdateNewChat) error {
	return func(c *gotdbot.Client, u *gotdbot.UpdateNewChat) error {
		if u.Chat != nil {
			chat := u.Chat
			dbChat := &models.Chat{
				ID:       chat.Id,
				ChatType: chat.Type.GetType(),
				Title:    chat.Title,
			}
			go func() {
				_ = database.UpsertChat(context.Background(), dbChat)
			}()
		}
		return nil
	}
}

func TrackUser(database *db.DB) func(c *gotdbot.Client, u *gotdbot.UpdateUser) error {
	return func(c *gotdbot.Client, u *gotdbot.UpdateUser) error {
		if u.User != nil {
			user := u.User
			dbUser := &models.Chat{
				ID:       user.Id,
				ChatType: "chatTypePrivate",
				Title:    user.FirstName + " " + user.LastName,
			}

			go func() {
				_ = database.UpsertChat(context.Background(), dbUser)
			}()

		}
		return nil
	}
}
