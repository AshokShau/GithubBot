package utils

import (
	"time"

	"github-webhook/internal/cache"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func IsAdmin(b *gotgbot.Bot, chatID int64, userID int64, adminCache *cache.Cache[int64, []int64]) bool {
	if admins, ok := adminCache.Get(chatID); ok {
		for _, adminID := range admins {
			if adminID == userID {
				return true
			}
		}
		return false
	}

	admins, err := b.GetChatAdministrators(chatID, nil)
	if err != nil {
		member, err := b.GetChatMember(chatID, userID, nil)
		if err != nil {
			return false
		}

		status := member.GetStatus()
		return status == "administrator" || status == "creator"
	}

	var adminIDs []int64
	isAdmin := false
	for _, admin := range admins {
		id := admin.GetUser().Id
		adminIDs = append(adminIDs, id)
		if id == userID {
			isAdmin = true
		}
	}

	adminCache.Set(chatID, adminIDs, 1*time.Hour)
	return isAdmin
}
