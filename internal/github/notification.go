package github

import (
	"cmp"
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github-webhook/internal/utils"

	"github.com/AshokShau/gotdbot"
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
		heading += " · " + utils.Clip(strings.ReplaceAll(action, "_", " "), 100)
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
	b.WriteString("<b>" + html.EscapeString(heading) + "</b>\n\n")
	if unsupportedAction {
		lineText := "Action received: " + utils.Clip(action, 100) + ". Open GitHub for details."
		b.WriteString(html.EscapeString(lineText) + "\n\n")
	}
	repo := p.object("repository")
	repoName := cmp.Or(repo.text("full_name"), repo.text("name"))
	repoURL := utils.SafeURL(repo.text("html_url"))
	if repoURL == "" && repositoryNamePattern.MatchString(repoName) && !strings.HasSuffix(repoName, "/.") && !strings.HasSuffix(repoName, "/..") {
		repoURL = "https://github.com/" + repoName
	}
	line := func(label, value string) {
		if value != "" {
			b.WriteString("<b>" + label + ":</b> " + value + "\n")
		}
	}
	line("Repository", utils.Link(utils.Clip(repoName, 180), repoURL))
	actor := cmp.Or(p.text("sender.login"), p.text("pusher.name"), p.text("sender.name"))
	if actor != "" {
		line("Actor", utils.Link(utils.Clip(actor, 100), p.text("sender.html_url")))
	}
	targetURL := firstURL(resource.text("html_url"), resource.text("web_url"), resource.text("target_url"), p.text("workflow_job.html_url"), p.text("deployment_status.target_url"), repoURL)
	title := resource.text("title")
	if name == "release" {
		title = cmp.Or(title, resource.text("name"))
	}
	if number := resource.text("number"); number != "" && number != "0" {
		title = "#" + number + " " + title
	}
	line("Title", html.EscapeString(utils.Clip(strings.TrimSpace(title), 240)))

	// Parent context remains visible for comments and reviews.
	for _, key := range []string{"issue", "pull_request", "discussion"} {
		parent := p.object(key)
		if key != spec.resource && parent != nil {
			label := parent.text("title")
			if n := parent.text("number"); n != "" {
				label = strings.TrimSpace("#" + n + " " + label)
			}
			parentLabel := strings.ToUpper(key[:1]) + strings.ReplaceAll(key[1:], "_", " ")
			if key == "issue" && parent.object("pull_request") != nil {
				parentLabel = "Pull request"
			}
			line(parentLabel, utils.Link(utils.Clip(label, 240), parent.text("html_url")))
			if targetURL == repoURL {
				targetURL = firstURL(parent.text("html_url"), targetURL)
			}
		}
	}
	var excerpt string
	for _, field := range spec.fields {
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
		ref := p.text("ref")
		line("Reference", utils.Code(utils.Clip(ref, 200)))
		for _, flag := range []struct{ key, label string }{{"created", "Reference created"}, {"deleted", "Reference deleted"}, {"forced", "Force push"}} {
			if p.value(flag.key) == true {
				line("Change", flag.label)
			}
		}
		commits := p.list("commits")
		if len(commits) == 0 && p.object("head_commit") != nil {
			commits = []any{map[string]any(p.object("head_commit"))}
		}
		count := cmp.Or(p.text("size"), fmt.Sprint(len(commits)))
		line("Commits", html.EscapeString(count))
		for i, c := range commits {
			if i == 5 {
				b.WriteString(fmt.Sprintf("… %d more commits in this payload.\n", len(commits)-i))
				break
			}
			m, ok := c.(map[string]any)
			if !ok {
				continue
			}
			commit := payload(m)
			sha := commit.text("id")
			subject, _, _ := strings.Cut(commit.text("message"), "\n")
			author := cmp.Or(commit.text("author.username"), commit.text("author.name"))
			commitURL := commit.text("url")
			if commitURL == "" && repoURL != "" && sha != "" {
				commitURL = repoURL + "/commit/" + sha
			}
			b.WriteString("• " + utils.Link(utils.ShortSHA(sha), commitURL) + " " + html.EscapeString(utils.Clip(subject, 220)))
			if author != "" {
				b.WriteString(" — " + html.EscapeString(utils.Clip(author, 80)))
			}
			b.WriteByte('\n')
		}
		targetURL = firstURL(p.text("compare"), repoURL)
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
	case "repository":
		line("Previous name", utils.Code(p.text("changes.repository.name.from")))
	}

	header := utils.LimitHTML(strings.TrimSpace(b.String()), 3400)
	if targetURL != "" {
		header += "\n\n" + utils.Link("View on GitHub", targetURL)
	}
	header = "<p>" + strings.ReplaceAll(header, "\n", "<br>") + "</p>"
	message := &gotdbot.InputRichMessage{Source: &gotdbot.RichMessageSourceHtml{Text: header}}
	if strings.TrimSpace(excerpt) != "" {
		message.Source = &gotdbot.RichMessageSourceMarkdown{Text: header + "\n\n" + utils.Clip(excerpt, 8000)}
	}

	if targetURL == "" {
		return message, nil
	}
	return message, &gotdbot.ReplyMarkupInlineKeyboard{Rows: [][]gotdbot.InlineKeyboardButton{{{Text: "Open on GitHub", Type: &gotdbot.InlineKeyboardButtonTypeUrl{Url: targetURL}}}}}
}
