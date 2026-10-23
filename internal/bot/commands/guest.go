package commands

import (
	"errors"

	"github.com/AshokShau/gotdbot"
)

// Guest introduces the bot without treating guest context as a linked chat or command.
func (h *CommandHandler) Guest(c *gotdbot.Client, u *gotdbot.UpdateNewGuestQuery) error {
	if u == nil || u.Message == nil {
		return nil
	}

	if c.Me == nil || c.Me().Usernames == nil || len(c.Me().Usernames.ActiveUsernames) == 0 {
		return errors.New("cannot answer guest query: bot username is unavailable")
	}

	botURL := "https://t.me/" + c.Me().Usernames.ActiveUsernames[0]
	message := `<h3>GitHub, meet Telegram</h3><p>Pull requests, issues, releases, and workflow updates in one place.</p><p>Add me to your group, connect GitHub with /connect in private, then use /addrepo in the group.</p><tg-button-row align="left"><tg-button type="url" url="` + botURL + `?startgroup=true">Add to group</tg-button><tg-button type="url" url="` + botURL + `?start=guest">Open bot</tg-button></tg-button-row>`
	_, err := u.Answer(c, &gotdbot.InputInlineQueryResultArticle{
		Id:    "github-introduction",
		Title: "GitHub, meet Telegram",
		InputMessageContent: &gotdbot.InputMessageRichMessage{
			Message: &gotdbot.InputRichMessage{
				Source: &gotdbot.RichMessageSourceHtml{Text: message},
			},
		},
	})
	return err
}
