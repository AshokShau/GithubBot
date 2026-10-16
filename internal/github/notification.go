package github

import (
	"cmp"
	"encoding/json"
	"fmt"
	"html"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
	gh "github.com/google/go-github/v92/github"
)

type eventSpec struct {
	title, resource string
	fields          []string
}

var eventSpecs = map[string]eventSpec{
	"status":                          {"Commit status", "", nil},
	"push":                            {"Push", "", nil},
	"issues":                          {"Issue", "issue", []string{"state", "state_reason"}},
	"pull_request":                    {"Pull request", "pull_request", []string{"state", "head.ref", "base.ref"}},
	"pull_request_target":             {"Pull request target", "pull_request", []string{"state", "head.ref", "base.ref"}},
	"issue_comment":                   {"Issue comment", "comment", []string{"body"}},
	"pull_request_review":             {"Pull request review", "review", []string{"state", "body"}},
	"pull_request_review_comment":     {"Review comment", "comment", []string{"path", "body"}},
	"pull_request_review_thread":      {"Review thread", "thread", []string{"path"}},
	"commit_comment":                  {"Commit comment", "comment", []string{"commit_id", "path", "body"}},
	"workflow_run":                    {"Workflow run", "workflow_run", []string{"name", "run_number", "head_branch", "status", "conclusion", "event"}},
	"workflow_job":                    {"Workflow job", "workflow_job", []string{"name", "workflow_name", "head_branch", "run_id", "status", "conclusion"}},
	"workflow_dispatch":               {"Workflow requested", "", []string{"workflow", "ref"}},
	"check_run":                       {"Check run", "check_run", []string{"name", "head_sha", "status", "conclusion", "output.title", "output.summary"}},
	"check_suite":                     {"Check suite", "check_suite", []string{"app.name", "head_branch", "head_sha", "status", "conclusion"}},
	"release":                         {"Release", "release", []string{"tag_name", "target_commitish", "body"}},
	"discussion":                      {"Discussion", "discussion", []string{"category.name", "state", "body"}},
	"discussion_comment":              {"Discussion comment", "comment", []string{"body"}},
	"deployment":                      {"Deployment", "deployment", []string{"environment", "ref", "sha", "description"}},
	"deployment_status":               {"Deployment status", "deployment_status", []string{"state", "description", "environment"}},
	"deployment_protection_rule":      {"Deployment protection rule", "", []string{"environment", "event"}},
	"deployment_review":               {"Deployment review", "", []string{"workflow_job_run.environment", "workflow_job_run.status"}},
	"repository":                      {"Repository", "repository", []string{"description", "default_branch", "visibility"}},
	"repository_import":               {"Repository import", "", []string{"status"}},
	"repository_dispatch":             {"Repository dispatch", "", nil},
	"repository_ruleset":              {"Repository ruleset", "repository_ruleset", []string{"name", "enforcement", "target"}},
	"repository_vulnerability_alert":  {"Vulnerability alert", "alert", []string{"affected_package_name", "affected_range", "fixed_in"}},
	"security_advisory":               {"Security advisory", "security_advisory", []string{"summary", "ghsa_id", "severity"}},
	"dependabot_alert":                {"Dependabot alert", "alert", []string{"state", "dependency.package.name", "security_advisory.summary", "security_advisory.severity"}},
	"code_scanning_alert":             {"Code scanning alert", "alert", []string{"state", "rule.name", "rule.severity", "rule.description", "most_recent_instance.ref"}},
	"secret_scanning_alert":           {"Secret scanning alert", "alert", []string{"state", "secret_type_display_name", "resolution"}},
	"secret_scanning_alert_location":  {"Secret scanning alert location", "alert", []string{"state", "secret_type_display_name"}},
	"security_and_analysis":           {"Security settings updated", "", nil},
	"branch_protection_rule":          {"Branch protection rule", "rule", []string{"name", "pattern"}},
	"branch_protection_configuration": {"Branch protection configuration", "", []string{"ref"}},
	"create":                          {"Reference created", "", []string{"ref_type", "ref", "description"}},
	"delete":                          {"Reference deleted", "", []string{"ref_type", "ref"}},
	"fork":                            {"Repository forked", "forkee", []string{"full_name", "description"}},
	"star":                            {"Star", "", nil},
	"watch":                           {"Watch", "", nil},
	"public":                          {"Repository made public", "", nil},
	"member":                          {"Collaborator", "", []string{"member.login"}},
	"membership":                      {"Team membership", "", []string{"member.login", "team.name", "scope"}},
	"team":                            {"Team", "team", []string{"name", "description", "privacy"}},
	"team_add":                        {"Team added", "team", []string{"name"}},
	"organization":                    {"Organization", "organization", []string{"login", "description"}},
	"org_block":                       {"Organization block", "", []string{"blocked_user.login", "organization.login"}},
	"user":                            {"User", "user", []string{"login"}},
	"label":                           {"Label", "label", []string{"name", "description", "color"}},
	"milestone":                       {"Milestone", "milestone", []string{"state", "description", "due_on"}},
	"meta":                            {"Webhook", "", []string{"hook_id"}},
	"ping":                            {"Webhook connected", "", nil},
	"deploy_key":                      {"Deploy key", "key", []string{"title"}},
	"page_build":                      {"Pages build", "build", []string{"status", "commit", "error.message"}},
	"gollum":                          {"Wiki updated", "", nil},
	"package":                         {"Package", "package", []string{"name", "package_type", "package_version.name"}},
	"registry_package":                {"Registry package", "registry_package", []string{"name", "package_type", "package_version.name"}},
	"merge_group":                     {"Merge group", "merge_group", []string{"head_ref", "base_ref", "head_sha"}},
	"installation":                    {"GitHub App installation", "installation", []string{"app_slug", "account.login", "repository_selection"}},
	"installation_repositories":       {"Installation repositories", "installation", []string{"account.login", "repository_selection"}},
	"installation_target":             {"Installation target", "", []string{"account.login", "target_type"}},
	"github_app_authorization":        {"GitHub App authorization", "", nil},
	"personal_access_token_request":   {"Access token request", "personal_access_token_request", []string{"owner.login", "token_name"}},
	"projects_v2":                     {"Project", "projects_v2", []string{"title", "number"}},
	"projects_v2_item":                {"Project item", "projects_v2_item", []string{"project_node_id", "content_type"}},
	"content_reference":               {"Content reference", "content_reference", []string{"reference"}},
	"custom_property":                 {"Custom property", "definition", []string{"property_name", "value_type", "description"}},
	"custom_property_values":          {"Custom property values updated", "", nil},
	"sponsorship":                     {"Sponsorship", "sponsorship", []string{"sponsor.login", "maintainer.login", "tier.name"}},
	"marketplace_purchase":            {"Marketplace purchase", "marketplace_purchase", []string{"account.login", "plan.name", "billing_cycle"}},
}

type payload map[string]any

func (p payload) value(path string) any {
	var v any = map[string]any(p)
	for key := range strings.SplitSeq(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}
func (p payload) text(path string) string {
	switch v := p.value(path).(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	}
	return ""
}
func (p payload) object(path string) payload {
	m, _ := p.value(path).(map[string]any)
	return m
}
func (p payload) list(path string) []any { a, _ := p.value(path).([]any); return a }
func firstURL(values ...string) string {
	for _, v := range values {
		if safe := utils.SafeURL(v); safe != "" {
			return safe
		}
	}
	return ""
}

var repositoryNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9_.-]+$`)
var commitTrailerPattern = regexp.MustCompile(`^[A-Za-z0-9-]+:\s*.+$`)
var githubNoreplyPattern = regexp.MustCompile(`(?i)^(?:[0-9]+\+)?([a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\[bot\])?)@users\.noreply\.github\.com$`)

func parseCommitMessage(message string) (subject, description string, coauthors []*gh.CommitAuthor) {
	subject, description, _ = strings.Cut(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), "\n")
	description = strings.TrimSpace(description)
	lines := strings.Split(description, "\n")
	start := len(lines)
	for start > 0 && commitTrailerPattern.MatchString(lines[start-1]) {
		start--
	}
	if start == len(lines) || start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		return
	}
	kept := append([]string(nil), lines[:start]...)
	for _, line := range lines[start:] {
		key, value, _ := strings.Cut(line, ":")
		if strings.EqualFold(key, "Co-authored-by") {
			address, err := mail.ParseAddress(strings.TrimSpace(value))
			if err == nil && address.Name != "" && strings.HasSuffix(strings.TrimSpace(value), ">") {
				coauthors = append(coauthors, &gh.CommitAuthor{Name: gh.Ptr(address.Name), Email: gh.Ptr(address.Address)})
				continue
			}
		}
		kept = append(kept, line)
	}
	description = strings.TrimSpace(strings.Join(kept, "\n"))
	return
}

func formatEvent(name string, body []byte) (*gotdbot.InputRichMessage, *gotdbot.ReplyMarkupInlineKeyboard) {
	spec, ok := eventSpecs[name]
	if !ok {
		return nil, nil
	}
	var p payload
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&p) != nil || p == nil {
		return nil, nil
	}

	resource := p
	if spec.resource != "" {
		resource = p.object(spec.resource)
	}
	action := p.text("action")
	if (name == "pull_request" || name == "pull_request_target") && action == "closed" && resource.value("merged") == true {
		action = "merged"
	}

	unsupportedAction := !knownAction(name, p.text("action"))
	heading := spec.title
	if name == "issue_comment" && p.object("issue.pull_request") != nil {
		heading = "Pull request comment"
	}

	if action != "" && !unsupportedAction {
		heading += " " + utils.Clip(strings.ReplaceAll(action, "_", " "), 100)
	}
	if name == "watch" && action == "started" {
		heading = "Repository watched"
	}
	if name == "star" {
		if action == "created" {
			heading = "Star added"
		}
		if action == "deleted" {
			heading = "Star removed"
		}
	}
	var b strings.Builder
	repo := p.object("repository")
	repoName := cmp.Or(repo.text("full_name"), repo.text("name"))
	repoURL := utils.SafeURL(repo.text("html_url"))
	if repoURL == "" && repositoryNamePattern.MatchString(repoName) && !strings.HasSuffix(repoName, "/.") && !strings.HasSuffix(repoName, "/..") {
		repoURL = "https://github.com/" + repoName
	}
	line := func(label, value string) {
		if value != "" {
			b.WriteString(label + ": " + value + "\n")
		}
	}
	actor := cmp.Or(p.text("sender.login"), p.text("pusher.name"), p.text("sender.name"))
	targetURL := firstURL(resource.text("html_url"), resource.text("web_url"), resource.text("target_url"), p.text("workflow_job.html_url"), p.text("deployment_status.target_url"), p.text("pull_request.html_url"), p.text("issue.html_url"), p.text("discussion.html_url"), repoURL)
	buttonLabel := map[string]string{
		"issues": "View Issue", "issue_comment": "View Comment", "pull_request": "View Pull Request", "pull_request_target": "View Pull Request",
		"pull_request_review": "View Review", "pull_request_review_comment": "View Comment", "pull_request_review_thread": "View Thread", "commit_comment": "View Comment",
		"workflow_run": "View Workflow", "workflow_job": "View Job", "workflow_dispatch": "View Actions", "check_run": "View Check", "check_suite": "View Checks",
		"release": "View Release", "discussion": "View Discussion", "discussion_comment": "View Comment", "deployment": "View Deployment", "deployment_status": "View Deployment",
		"repository": "View Repository", "fork": "View Fork", "star": "View Repository", "watch": "View Repository", "public": "View Repository", "gollum": "View Wiki",
		"label": "View Label", "milestone": "View Milestone",
	}[name]
	if buttonLabel == "" {
		buttonLabel = "View on GitHub"
	}
	b.WriteString("<b>" + utils.Link(heading, targetURL) + "</b>\n")
	if unsupportedAction {
		b.WriteString(html.EscapeString("Action received: "+utils.Clip(action, 100)+". Open GitHub for details.") + "\n")
	}
	title := resource.text("title")
	if name == "release" {
		title = cmp.Or(title, resource.text("name"))
	}
	if name == "workflow_run" || name == "workflow_job" || name == "check_run" {
		title = resource.text("name")
		if run := resource.text("run_number"); run != "" && run != "0" {
			title += " #" + run
		}
	}
	if number := resource.text("number"); number != "" && number != "0" {
		title = "#" + number + " " + title
	}
	if title = strings.TrimSpace(title); title != "" {
		b.WriteString(utils.Link(utils.Clip(title, 240), targetURL) + "\n")
	}
	if repoName != "" {
		b.WriteString(utils.Link(utils.Clip(repoName, 180), repoURL))
	}
	if actor != "" {
		if repoName != "" {
			b.WriteString(" · ")
		}
		b.WriteString("by " + utils.Link(utils.Clip(actor, 100), p.text("sender.html_url")))
	}
	b.WriteString("\n\n")

	// Parent context remains visible for comments and reviews.
	for _, key := range []string{"issue", "pull_request", "discussion"} {
		parent := p.object(key)
		if key != spec.resource && parent != nil {
			label := parent.text("title")
			if n := parent.text("number"); n != "" {
				label = strings.TrimSpace("#" + n + " " + label)
			}
			if label != "" {
				b.WriteString(utils.Link(utils.Clip(label, 240), firstURL(parent.text("html_url"), targetURL)) + "\n")
			}
			if targetURL == repoURL {
				targetURL = firstURL(parent.text("html_url"), targetURL)
			}
		}
	}
	var excerpt string
	for _, field := range spec.fields {
		if (field == "name" || field == "run_number") && (name == "workflow_run" || name == "workflow_job" || name == "check_run") {
			continue
		}
		if (field == "head.ref" || field == "base.ref") && (name == "pull_request" || name == "pull_request_target") {
			continue
		}
		if field == "body" || field == "output.summary" {
			excerpt, _ = resource.value(field).(string)
			continue
		}
		value := resource.text(field)
		if value == "" {
			continue
		}
		label := map[string]string{"head.ref": "Source branch", "base.ref": "Target branch", "head_branch": "Branch", "target_commitish": "Target", "name": "Name", "state": "State", "run_number": "Run", "sha": "Commit", "head_sha": "Commit", "commit_id": "Commit"}[field]
		if label == "" {
			parts := strings.Split(field, ".")
			label = strings.ReplaceAll(parts[len(parts)-1], "_", " ")
			label = strings.ToUpper(label[:1]) + label[1:]
		}
		if field == "title" && title != "" {
			continue
		}
		value = utils.Clip(value, 200)
		if strings.Contains(field, "summary") || strings.Contains(field, "description") {
			line(label, html.EscapeString(value))
		} else {
			line(label, utils.Code(value))
		}
	}
	if name == "issues" || name == "pull_request" || name == "pull_request_target" {
		if source, target := resource.text("head.ref"), resource.text("base.ref"); source != "" && target != "" {
			b.WriteString(utils.Code(utils.Clip(source, 160)) + " → " + utils.Code(utils.Clip(target, 160)) + "\n")
		} else {
			line("Source branch", utils.Code(utils.Clip(source, 160)))
			line("Target branch", utils.Code(utils.Clip(target, 160)))
		}
		for _, item := range []struct{ key, label string }{{"assignee.login", "Assignee"}, {"label.name", "Label"}, {"milestone.title", "Milestone"}, {"requested_reviewer.login", "Reviewer"}, {"requested_team.name", "Review team"}} {
			if v := p.text(item.key); v != "" {
				line(item.label, utils.Code(utils.Clip(v, 160)))
			}
		}
		if action == "opened" || action == "edited" {
			excerpt, _ = resource.value("body").(string)
		}
	}
	switch name {
	case "push":
		var push gh.PushEvent
		if json.Unmarshal(body, &push) != nil {
			return nil, nil
		}
		b.Reset()
		ref := push.GetRef()
		branch := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
		referenceType := "Reference"
		if strings.HasPrefix(ref, "refs/heads/") {
			referenceType = "Branch"
		} else if strings.HasPrefix(ref, "refs/tags/") {
			referenceType = "Tag"
		}
		commits := make([]*gh.HeadCommit, 0, len(push.GetCommits()))
		if !push.GetDeleted() {
			for _, commit := range push.GetCommits() {
				if commit != nil && (commit.GetID() != "" || commit.GetSHA() != "" || strings.TrimSpace(commit.GetMessage()) != "") {
					commits = append(commits, commit)
				}
			}
			if head := push.GetHeadCommit(); len(commits) == 0 && head != nil && (head.GetID() != "" || head.GetSHA() != "" || strings.TrimSpace(head.GetMessage()) != "") {
				commits = append(commits, head)
			}
		}
		count := max(len(commits), push.GetSize())
		heading = "Push to"
		switch {
		case push.GetDeleted():
			heading = referenceType + " deleted in"
		case count == 1:
			heading = "1 new commit to"
		case count > 1:
			heading = fmt.Sprintf("%d new commits to", count)
		case push.GetCreated():
			heading = referenceType + " created in"
		}
		referenceLabel, referenceURL := repoName, repoURL
		if branch != "" {
			if referenceLabel != "" {
				referenceLabel += ":"
			}
			referenceLabel += branch
			if repoURL != "" && !push.GetDeleted() {
				referenceURL = repoURL + "/tree/" + url.PathEscape(branch)
			}
		}
		b.WriteString("<p><b>" + heading + "</b>")
		if referenceLabel != "" {
			b.WriteString(" " + utils.Link(utils.Clip(referenceLabel, 240), referenceURL))
		}
		var changes []string
		if push.GetForced() {
			changes = append(changes, "Force push")
		}
		if push.GetCreated() && count > 0 && !push.GetDeleted() {
			changes = append(changes, referenceType+" created")
		}
		if len(changes) > 0 {
			b.WriteString("<br><i>" + strings.Join(changes, " · ") + "</i>")
		}
		b.WriteString("</p>\n\n")
		shown := min(len(commits), 5)
		actorCredited := false
		for _, commit := range commits[:shown] {
			sha := cmp.Or(commit.GetID(), commit.GetSHA())
			subject, description, coauthors := parseCommitMessage(commit.GetMessage())
			commitURL := firstURL(commit.GetURL())
			if commitURL == "" && repoURL != "" && sha != "" {
				commitURL = repoURL + "/commit/" + url.PathEscape(sha)
			}
			b.WriteString("\\- ")
			if sha != "" {
				b.WriteString(utils.Link(utils.ShortSHA(sha), commitURL))
				if subject != "" {
					b.WriteString(": ")
				}
			}
			b.WriteString("<b>" + utils.GitHubMarkdown(utils.Clip(subject, 220), repoURL) + "</b>")
			var credits []string
			seen := make(map[string]bool)
			contributors := append([]*gh.CommitAuthor{commit.GetAuthor()}, coauthors...)
			unique := 0
			for i, contributor := range contributors {
				login := contributor.GetLogin()
				if login == "" {
					if match := githubNoreplyPattern.FindStringSubmatch(contributor.GetEmail()); match != nil {
						login = match[1]
					}
				}
				label := cmp.Or(contributor.GetName(), login)
				if i == 0 {
					label = cmp.Or(login, contributor.GetName())
				}
				if label == "" {
					continue
				}
				emailKey, loginKey := "email:"+strings.ToLower(contributor.GetEmail()), "login:"+strings.ToLower(login)
				if contributor.GetEmail() != "" && seen[emailKey] || login != "" && seen[loginKey] || contributor.GetEmail() == "" && login == "" && seen["name:"+strings.ToLower(label)] {
					continue
				}
				seen[emailKey], seen[loginKey], seen["name:"+strings.ToLower(label)] = true, true, true
				unique++
				if len(credits) == 3 {
					continue
				}
				authorURL := ""
				if login != "" {
					authorURL = "https://github.com/" + url.PathEscape(login)
				}
				limit := 40
				if i == 0 {
					limit = 80
				}
				label = (&gotdbot.FormattedText{Text: utils.Clip(label, limit)}).Format("MarkdownV2")
				// Link escapes > as an HTML entity; other Markdown punctuation stays escaped.
				label = strings.ReplaceAll(label, "\\>", ">")
				credits = append(credits, utils.Link(label, authorURL))
				actorCredited = actorCredited || actor != "" && (strings.EqualFold(actor, login) || login == "" && strings.EqualFold(actor, contributor.GetName()))
			}
			if len(credits) > 0 {
				b.WriteString(" by " + strings.Join(credits, ", "))
				if unique > len(credits) {
					noun := "co-authors"
					if unique-len(credits) == 1 {
						noun = "co-author"
					}
					b.WriteString(fmt.Sprintf(" +%d %s", unique-len(credits), noun))
				}
			}
			b.WriteString("\n\n")
			if description != "" {
				b.WriteString("<details><summary><sup>ᴅᴇꜱᴄʀɪᴘᴛɪᴏɴ</sup></summary>\n\n" +
					utils.GitHubMarkdown(utils.Clip(description, 1000/shown), repoURL) +
					"\n\n</details>\n\n")
			}
		}
		if remaining := count - shown; remaining > 0 && !push.GetDeleted() {
			noun := "commits"
			if remaining == 1 {
				noun = "commit"
			}
			b.WriteString(fmt.Sprintf("<p>… %d more %s.</p>\n", remaining, noun))
		}
		if actor != "" && !actorCredited {
			if len(commits) > 0 || count > 0 && !push.GetDeleted() {
				b.WriteByte('\n')
			}
			verb := "Pushed"
			if push.GetDeleted() {
				verb = "Deleted"
			} else if push.GetCreated() && count == 0 {
				verb = "Created"
			}
			b.WriteString("<p>" + verb + " by " + utils.Link(utils.Clip(actor, 100), p.text("sender.html_url")) + "</p>\n")
		}
		buttonLabel = "Compare Changes"
		targetURL = firstURL(push.GetCompare(), referenceURL, repoURL)
		if push.GetDeleted() {
			buttonLabel, targetURL = "View Repository", repoURL
		} else if count == 1 && len(commits) == 1 {
			commitURL := firstURL(commits[0].GetURL())
			if commitURL == "" && repoURL != "" {
				if sha := cmp.Or(commits[0].GetID(), commits[0].GetSHA()); sha != "" {
					commitURL = repoURL + "/commit/" + url.PathEscape(sha)
				}
			}
			if commitURL != "" {
				buttonLabel, targetURL = "View Commit", commitURL
			}
		} else if count == 0 {
			buttonLabel, targetURL = "View Repository", repoURL
		}
	case "gollum":
		pages := p.list("pages")
		for i, page := range pages {
			if i == 5 {
				b.WriteString("More pages are available on GitHub.\n")
				break
			}
			m, _ := page.(map[string]any)
			pg := payload(m)
			line("Page", utils.Link(utils.Clip(pg.text("title"), 180), pg.text("html_url"))+" · "+html.EscapeString(pg.text("action")))
		}
		if repoURL != "" {
			targetURL = repoURL + "/wiki"
		}
	case "deployment_status":
		line("Environment", utils.Code(p.text("deployment.environment")))
		line("Reference", utils.Code(p.text("deployment.ref")))
		targetURL = firstURL(p.text("deployment_status.environment_url"), p.text("deployment_status.log_url"), targetURL)
	case "status":
		line("Context", html.EscapeString(utils.Clip(p.text("context"), 200)))
		line("State", utils.Code(p.text("state")))
		line("Description", html.EscapeString(utils.Clip(p.text("description"), 400)))
		line("Commit", utils.Code(utils.ShortSHA(p.text("sha"))))
		targetURL = firstURL(p.text("target_url"), p.text("commit.html_url"), repoURL)
	case "installation_repositories":
		for _, key := range []string{"repositories_added", "repositories_removed"} {
			for i, v := range p.list(key) {
				if i == 5 {
					line(strings.ReplaceAll(key, "_", " "), "More repositories are available on GitHub.")
					break
				}
				m, _ := v.(map[string]any)
				line(strings.ReplaceAll(key, "_", " "), utils.Code(utils.Clip(payload(m).text("full_name"), 160)))
			}
		}
	case "release":
		if resource.value("prerelease") == true {
			line("Type", "Prerelease")
		}
		if resource.value("draft") == true {
			line("State", "Draft")
		}
	case "star":
		line("Stars", html.EscapeString(repo.text("stargazers_count")))
	case "watch":
		line("Watchers", html.EscapeString(repo.text("subscribers_count")))
	case "repository":
		line("Previous name", utils.Code(p.text("changes.repository.name.from")))
	}

	header := strings.TrimSpace(b.String())
	if name != "push" {
		header = utils.LimitHTML(header, 3400)
		header = "<p>" + strings.ReplaceAll(header, "\n", "<br>") + "</p>"
	}
	message := &gotdbot.InputRichMessage{Source: &gotdbot.RichMessageSourceHtml{Text: header}}
	if name == "push" {
		message.Source = &gotdbot.RichMessageSourceMarkdown{Text: header}
	} else if strings.TrimSpace(excerpt) != "" {
		message.Source = &gotdbot.RichMessageSourceMarkdown{Text: header + "\n\n" + utils.GitHubMarkdown(utils.Clip(excerpt, 8000), repoURL)}
	}

	if targetURL == "" {
		return message, nil
	}
	return message, &gotdbot.ReplyMarkupInlineKeyboard{Rows: [][]gotdbot.InlineKeyboardButton{{{Text: buttonLabel, Type: &gotdbot.InlineKeyboardButtonTypeUrl{Url: targetURL}}}}}
}
