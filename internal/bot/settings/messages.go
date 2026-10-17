package settings

import (
	"fmt"
	"html"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	gh "github.com/google/go-github/v92/github"

	"github-webhook/internal/github"
	"github-webhook/internal/models"
	"github-webhook/internal/utils"
)

const pageSize = 6

// DiscoveryPage is shared by the initial /addrepo reply and its pagination edits.
func DiscoveryPage(repos []*gh.Repository, response *gh.Response, page int) string {
	page = max(page, 1)
	repos = slices.Clone(repos)
	repos = slices.DeleteFunc(repos, func(repo *gh.Repository) bool { return repo.GetID() <= 0 || repo.GetFullName() == "" })
	slices.SortFunc(repos, func(a, b *gh.Repository) int { return strings.Compare(a.GetFullName(), b.GetFullName()) })
	var b strings.Builder
	b.WriteString("<h3>Link a repository</h3>")
	if len(repos) == 0 {
		if page > 1 {
			b.WriteString("<p>No accessible repositories on this page. Use Previous, or check your GitHub account's repository permissions.</p>")
		} else {
			b.WriteString("<p>No accessible repositories found. Check the connected GitHub account and its repository permissions.</p>")
		}
	} else {
		b.WriteString("<p>Choose a repository to receive notifications here.</p>")
	}
	ownerGroup, inRow, rowWidth := "", 0, 0
	for _, repo := range repos {
		owner, name, ok := strings.Cut(repo.GetFullName(), "/")
		if !ok || owner == "" || name == "" {
			owner, name = "Repositories", repo.GetFullName()
		}
		label := utils.Clip(name, 100)
		width := utf8.RuneCountInString(label)
		if owner != ownerGroup || inRow != 0 && rowWidth+width > 32 {
			if inRow != 0 {
				b.WriteString("</tg-button-row>")
				inRow = 0
			}
		}
		if owner != ownerGroup {
			fmt.Fprintf(&b, "<p><b>%s</b></p>", html.EscapeString(utils.Clip(owner, 80)))
			ownerGroup = owner
		}
		if inRow == 0 {
			b.WriteString(`<tg-button-row align="left">`)
			rowWidth = 0
		}
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ar:id:%d:%d">%s</tg-button>`, repo.GetID(), page, html.EscapeString(label))
		inRow++
		rowWidth += width
		if inRow == 2 || width > 24 {
			b.WriteString("</tg-button-row>")
			inRow = 0
		}
	}
	if inRow != 0 {
		b.WriteString("</tg-button-row>")
	}
	prev, next, last := 0, 0, 0
	if response != nil {
		prev, next, last = response.PrevPage, response.NextPage, response.LastPage
	}
	if last > 1 {
		fmt.Fprintf(&b, "<footer>Page %d of %d</footer>", page, last)
	} else if page > 1 || next > 0 {
		fmt.Fprintf(&b, "<footer>Page %d</footer>", page)
	}
	if prev != 0 || next != 0 {
		b.WriteString(`<tg-button-row align="left">`)
		if prev != 0 {
			fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ar:pg:%d">Previous</tg-button>`, prev)
		} else {
			b.WriteString(`<tg-button type="disabled">Previous</tg-button>`)
		}
		if next != 0 {
			fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ar:pg:%d">Next</tg-button>`, next)
		} else {
			b.WriteString(`<tg-button type="disabled">Next</tg-button>`)
		}
		b.WriteString("</tg-button-row>")
	}
	return b.String()
}

func RepositoryLinked(repo string, hookID int64, page int, alreadyLinked bool) string {
	heading := "Repository linked"
	if alreadyLinked {
		heading = "Repository already linked"
	}
	settingsData := "c:ls"
	if hookID > 0 {
		settingsData = fmt.Sprintf("c:r:%d", hookID)
	}
	return fmt.Sprintf(`<h3>%s</h3><p>%s</p><p>Choose Settings to change notification events, or go back to link another repository.</p><tg-button-row align="left"><tg-button type="callback_data" data="c:ar:pg:%d">Back</tg-button><tg-button type="callback_data" data="%s">Settings</tg-button></tg-button-row>`,
		heading, utils.Code(repo), max(page, 1), settingsData)
}

// LinkedRepositoryPage keeps URL navigation in one rich message. A page of
// 100 bounds both visible text and blocks, including distinct owner headings.
func LinkedRepositoryPage(links []models.RepoLink, page int) string {
	if len(links) == 0 {
		return "<h3>Linked repositories</h3><p>No repositories linked. Use <code>/addrepo</code> to link one.</p>"
	}
	links = slices.Clone(links)
	slices.SortFunc(links, func(a, b models.RepoLink) int { return strings.Compare(a.RepoFullName, b.RepoFullName) })
	pages := (len(links) + 99) / 100
	page = min(max(page, 1), pages)
	start, end := (page-1)*100, min(page*100, len(links))
	var msg strings.Builder
	msg.WriteString("<h3>Linked repositories</h3>")
	collapsed := len(links) > 12
	if collapsed {
		fmt.Fprintf(&msg, "<details><summary>View repositories (%d)</summary>", end-start)
	}
	ownerGroup, inRow, rowWidth := "", 0, 0
	for _, link := range links[start:end] {
		owner, name, ok := strings.Cut(link.RepoFullName, "/")
		if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
			if inRow != 0 {
				msg.WriteString("</tg-button-row>")
				inRow = 0
			}
			fmt.Fprintf(&msg, "<p>%s</p>", utils.Code(utils.Clip(link.RepoFullName, 160)))
			continue
		}
		label := utils.Clip(name, 100)
		width := utf8.RuneCountInString(label)
		if owner != ownerGroup || inRow != 0 && rowWidth+width > 32 {
			if inRow != 0 {
				msg.WriteString("</tg-button-row>")
				inRow = 0
			}
		}
		if owner != ownerGroup {
			fmt.Fprintf(&msg, "<p><b>%s</b></p>", html.EscapeString(utils.Clip(owner, 80)))
			ownerGroup = owner
		}
		if inRow == 0 {
			msg.WriteString(`<tg-button-row align="left">`)
			rowWidth = 0
		}
		target := "https://github.com/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
		fmt.Fprintf(&msg, `<tg-button type="url" url="%s">%s</tg-button>`, html.EscapeString(target), html.EscapeString(label))
		inRow++
		rowWidth += width
		if inRow == 2 || width > 24 {
			msg.WriteString("</tg-button-row>")
			inRow = 0
		}
	}
	if inRow != 0 {
		msg.WriteString("</tg-button-row>")
	}
	if collapsed {
		msg.WriteString("</details>")
	}
	if pages > 1 {
		fmt.Fprintf(&msg, `<footer>Page %d of %d · %d repositories</footer><tg-button-row align="left">`, page, pages, len(links))
		if page > 1 {
			fmt.Fprintf(&msg, `<tg-button type="callback_data" data="c:rl:%d">Previous</tg-button>`, page-1)
		} else {
			msg.WriteString(`<tg-button type="disabled">Previous</tg-button>`)
		}
		if page < pages {
			fmt.Fprintf(&msg, `<tg-button type="callback_data" data="c:rl:%d">Next</tg-button>`, page+1)
		} else {
			msg.WriteString(`<tg-button type="disabled">Next</tg-button>`)
		}
		msg.WriteString("</tg-button-row>")
	}
	msg.WriteString("<footer>Use /settings to manage notifications.</footer>")
	return msg.String()
}

func RepositoryPage(links []models.RepoLink, page int) string {
	if len(links) == 0 {
		return "<h3>Notification settings</h3><p>No repositories linked. Use <code>/addrepo</code> to link one.</p>"
	}
	links = slices.Clone(links)
	slices.SortFunc(links, func(a, b models.RepoLink) int { return strings.Compare(a.RepoFullName, b.RepoFullName) })
	pages := (len(links) + pageSize - 1) / pageSize
	page = min(max(page, 1), pages)
	var b strings.Builder
	b.WriteString("<h3>Notification settings</h3><p>Choose a repository.</p>")
	ownerGroup, inRow, rowWidth := "", 0, 0
	for _, link := range links[(page-1)*pageSize : min(page*pageSize, len(links))] {
		owner, name, ok := strings.Cut(link.RepoFullName, "/")
		if !ok {
			owner, name = "Repositories", link.RepoFullName
		}
		labelWidth := utf8.RuneCountInString(name)
		if owner != ownerGroup || inRow != 0 && rowWidth+labelWidth > 32 {
			if inRow != 0 {
				b.WriteString("</tg-button-row>")
				inRow = 0
			}
		}
		if owner != ownerGroup {
			fmt.Fprintf(&b, "<p><b>%s</b></p>", html.EscapeString(owner))
			ownerGroup = owner
		}
		if inRow == 0 {
			b.WriteString(`<tg-button-row align="left">`)
			rowWidth = 0
		}
		key := link.RepoFullName
		if link.WebhookID > 0 {
			key = fmt.Sprint(link.WebhookID)
		}
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">%s</tg-button>`, html.EscapeString("c:r:"+key), html.EscapeString(name))
		inRow++
		rowWidth += labelWidth
		if inRow == 2 || labelWidth > 24 {
			b.WriteString("</tg-button-row>")
			inRow = 0
		}
	}
	if inRow != 0 {
		b.WriteString("</tg-button-row>")
	}
	if pages == 1 {
		return b.String()
	}
	fmt.Fprintf(&b, `<footer>Page %d of %d</footer><tg-button-row align="left">`, page, pages)
	if page > 1 {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ls:%d">Previous</tg-button>`, page-1)
	} else {
		b.WriteString(`<tg-button type="disabled">Previous</tg-button>`)
	}
	if page < pages {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="c:ls:%d">Next</tg-button>`, page+1)
	} else {
		b.WriteString(`<tg-button type="disabled">Next</tg-button>`)
	}
	b.WriteString("</tg-button-row>")
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
	fmt.Fprintf(&b, "<h3>Notification events</h3><p>%s</p><p>Select the events to receive.</p>", utils.Code(repo))
	start := (page - 1) * pageSize
	for i, event := range github.SupportedEvents[start:min(start+pageSize, len(github.SupportedEvents))] {
		if i%2 == 0 {
			b.WriteString(`<tg-button-row align="left">`)
		}
		status, style := "Off", ""
		if enabled[event.Name] {
			status, style = "On", ` style="success"`
		}
		fmt.Fprintf(&b, `<tg-button type="callback_data"%s data="%s">%s %s</tg-button>`, style,
			html.EscapeString(fmt.Sprintf("c:te:%s:%s:%d", key, event.Short, page)), status, html.EscapeString(event.Label))
		if i%2 == 1 || start+i == len(github.SupportedEvents)-1 {
			b.WriteString("</tg-button-row>")
		}
	}
	fmt.Fprintf(&b, `<footer>Page %d of %d</footer><tg-button-row align="left">`, page, pages)
	if page > 1 {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">Previous</tg-button>`, html.EscapeString(fmt.Sprintf("c:ep:%s:%d", key, page-1)))
	} else {
		b.WriteString(`<tg-button type="disabled">Previous</tg-button>`)
	}
	if page < pages {
		fmt.Fprintf(&b, `<tg-button type="callback_data" data="%s">Next</tg-button>`, html.EscapeString(fmt.Sprintf("c:ep:%s:%d", key, page+1)))
	} else {
		b.WriteString(`<tg-button type="disabled">Next</tg-button>`)
	}
	fmt.Fprintf(&b, `</tg-button-row><tg-button-row align="left"><tg-button type="callback_data" data="%s">Back</tg-button>`, html.EscapeString("c:r:"+key))
	fmt.Fprintf(&b, `<tg-button type="url" url="https://github.com/%s/settings/hooks/%d">GitHub settings</tg-button></tg-button-row>`, html.EscapeString(repo), hookID)
	b.WriteString("<footer>Changing a selection replaces an all-events subscription with individual events.</footer>")
	return b.String()
}
