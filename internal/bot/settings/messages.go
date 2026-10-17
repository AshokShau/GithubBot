package settings

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"
)

const pageSize = 6

func RepositoryPage(links []models.RepoLink, page int) string {
	if len(links) == 0 {
		return "<p><b>Notification settings</b></p><p>No repositories linked. Use <code>/addrepo</code> to link one.</p>"
	}
	links = slices.Clone(links)
	slices.SortFunc(links, func(a, b models.RepoLink) int { return strings.Compare(a.RepoFullName, b.RepoFullName) })
	pages := (len(links) + pageSize - 1) / pageSize
	page = min(max(page, 1), pages)
	var b strings.Builder
	fmt.Fprintf(&b, "<p><b>Notification settings</b></p><p>Choose a repository · Page %d of %d</p><p>", page, pages)
	for _, link := range links[(page-1)*pageSize : min(page*pageSize, len(links))] {
		key := link.RepoFullName
		if link.WebhookID > 0 {
			key = fmt.Sprint(link.WebhookID)
		}
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">%s</tg-button><br>`, html.EscapeString("c:r:"+key), html.EscapeString(link.RepoFullName))
	}
	b.WriteString("</p>")
	if pages == 1 {
		return b.String()
	}
	b.WriteString("<p>")
	if page > 1 {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ls:%d">Previous</tg-button> `, page-1)
	}
	if page < pages {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ls:%d">Next</tg-button>`, page+1)
	}
	b.WriteString("</p>")
	return b.String()
}

func EventsPage(repo string, hookID int64, enabled map[string]bool, page int) string {
	key := repo
	if hookID > 0 {
		key = fmt.Sprint(hookID)
	}
	pages := max(1, (len(github.SupportedEvents)+pageSize-1)/pageSize)
	page = min(max(page, 1), pages)
	var b strings.Builder
	fmt.Fprintf(&b, "<p><b>Notification events</b><br>%s</p><p>Page %d of %d · Select events to receive.</p><p>", utils.Code(repo), page, pages)
	start := (page - 1) * pageSize
	for i, event := range github.SupportedEvents[start:min(start+pageSize, len(github.SupportedEvents))] {
		status, style := "Off", ""
		if enabled[event.Name] {
			status, style = "On", ` style="success"`
		}
		fmt.Fprintf(&b, `<tg-button type="callback_data"%s data="%s">%s %s</tg-button> `, style,
			html.EscapeString(fmt.Sprintf("c:te:%s:%s:%d", key, event.Short, page)), status, html.EscapeString(event.Label))
		if i%2 == 1 {
			b.WriteString("<br>")
		}
	}
	b.WriteString("</p><p>")
	if page > 1 {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">Previous</tg-button> `, html.EscapeString(fmt.Sprintf("c:ep:%s:%d", key, page-1)))
	}
	if page < pages {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">Next</tg-button> `, html.EscapeString(fmt.Sprintf("c:ep:%s:%d", key, page+1)))
	}
	fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">Back</tg-button></p>`, html.EscapeString("c:r:"+key))
	fmt.Fprintf(&b, `<p><tg-button type="url" url="https://github.com/%s/settings/hooks/%d">GitHub settings</tg-button></p>`, html.EscapeString(repo), hookID)
	b.WriteString("<p>Changing a selection replaces an all-events subscription with individual events.</p>")
	return b.String()
}
