package utils

import (
	"slices"
	"time"

	"github-webhook/internal/cache"

	"github.com/AshokShau/gotdbot"
)

func IsAdmin(b *gotdbot.Client, chatID int64, userID int64, adminCache *cache.Cache[int64, []int64]) bool {
	if admins, ok := adminCache.Get(chatID); ok {
		return slices.Contains(admins, userID)
	}

	admins, err := b.GetChatAdministrators(chatID)
	if err != nil {
		b.Logger.Warnf("%v", err)
		return false
	}

	var adminIDs []int64
	isAdmin := false
	for _, admin := range admins.Administrators {
		id := admin.UserId
		adminIDs = append(adminIDs, id)
		if id == userID {
			isAdmin = true
		}
	}

	adminCache.Set(chatID, adminIDs, 1*time.Hour)
	return isAdmin
}
