package github

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/google/go-github/v81/github"
)

func FormatIssuesEvent(event *github.IssuesEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := event.GetRepo().GetFullName()
	action := event.GetAction()
	sender := event.GetSender().GetLogin()
	issue := event.GetIssue()
	title := issue.GetTitle()
	url := issue.GetHTMLURL()
	number := issue.GetNumber()

	msg := fmt.Sprintf(
		"*📌 %s issue \\#%d*\n"+
			"*Title:* %s\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(strings.Title(action)),
		number,
		EscapeMarkdownV2(title),
		FormatRepo(repo),
		FormatUser(sender),
	)

	switch action {
	case "opened", "edited":
		if body := issue.GetBody(); body != "" {
			msg += fmt.Sprintf("*Description:*\n%s\n", FormatTextWithMarkdown(body))
		}
	case "closed":
		if closer := issue.GetClosedBy(); closer != nil {
			msg += fmt.Sprintf("*Closed by:* %s\n", EscapeMarkdownV2(closer.GetLogin()))
		}
	case "reopened":
		msg += "_Issue reopened_\n"
	case "assigned":
		var assignees []string
		for _, a := range issue.Assignees {
			assignees = append(assignees, EscapeMarkdownV2(a.GetLogin()))
		}
		msg += fmt.Sprintf("*Assigned to:* %s\n", strings.Join(assignees, ", "))
	case "labeled":
		var labels []string
		for _, l := range issue.Labels {
			labels = append(labels, EscapeMarkdownV2(l.GetName()))
		}
		msg += fmt.Sprintf("*Labels:* %s\n", strings.Join(labels, ", "))
	case "milestoned":
		if m := issue.GetMilestone(); m != nil {
			msg += fmt.Sprintf("*Milestone:* %s\n", EscapeMarkdownV2(m.GetTitle()))
		}
	}

	return FormatMessageWithButton(msg, "View Issue", url)
}

func FormatPullRequestEvent(event *github.PullRequestEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := event.GetRepo().GetFullName()
	action := event.GetAction()
	sender := event.GetSender().GetLogin()
	pr := event.GetPullRequest()
	title := pr.GetTitle()
	url := pr.GetHTMLURL()
	state := pr.GetState()
	number := pr.GetNumber()

	msg := fmt.Sprintf(
		"*🚀 PR %s \\#%d: %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s \\| *State:* %s\n",
		EscapeMarkdownV2(strings.Title(action)),
		number,
		EscapeMarkdownV2(title),
		FormatRepo(repo),
		FormatUser(sender),
		EscapeMarkdownV2(state),
	)

	switch action {
	case "opened":
		msg += fmt.Sprintf("*Description:*\n%s\n", FormatTextWithMarkdown(pr.GetBody()))
	case "closed":
		if pr.GetMerged() {
			msg += "✅ Merged\n"
		} else {
			msg += "❌ Closed without merging\n"
		}
	case "reopened":
		msg += "🔄 Reopened\n"
	case "edited":
		msg += fmt.Sprintf("✏️ Edited\n*Description:*\n%s\n", FormatTextWithMarkdown(pr.GetBody()))
	case "assigned":
		var assignees []string
		for _, a := range pr.Assignees {
			assignees = append(assignees, EscapeMarkdownV2(a.GetLogin()))
		}
		msg += fmt.Sprintf("*Assigned:* %s\n", strings.Join(assignees, ", "))
	case "review_requested":
		var reviewers []string
		for _, r := range pr.RequestedReviewers {
			reviewers = append(reviewers, EscapeMarkdownV2(r.GetLogin()))
		}
		msg += fmt.Sprintf("*Reviewers:* %s\n", strings.Join(reviewers, ", "))
	case "labeled":
		var labels []string
		for _, l := range pr.Labels {
			labels = append(labels, EscapeMarkdownV2(l.GetName()))
		}
		msg += fmt.Sprintf("*Labels:* %s\n", strings.Join(labels, ", "))
	case "synchronize":
		msg += "🔄 New commits pushed\n"
	}

	return FormatMessageWithButton(msg, "View PR", url)
}

func FormatPushEvent(event *github.PushEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := event.Repo.GetName()
	repoURL := event.Repo.GetHTMLURL()
	branch := strings.TrimPrefix(event.GetRef(), "refs/heads/")
	compareURL := event.GetCompare()

	var commits []*github.HeadCommit
	if len(event.Commits) > 0 {
		commits = event.Commits
	} else if event.HeadCommit != nil {
		commits = []*github.HeadCommit{event.HeadCommit}
	}

	commitCount := len(commits)
	if commitCount == 0 {
		return "", nil
	}

	var commitPlural string
	if commitCount > 1 {
		commitPlural = "s"
	}
	msg := fmt.Sprintf(
		"🔨 *%d new commit%s to* `%s:%s`\n\n",
		commitCount, commitPlural, EscapeMarkdownV2(repo), EscapeMarkdownV2(branch),
	)

	if event.GetCreated() {
		msg += "🌱 _New branch created_\n"
	} else if event.GetDeleted() {
		msg += "🗑️ _Branch deleted_\n"
	} else if event.GetForced() {
		msg += "⚠️ _Force pushed_\n"
	}

	for _, commit := range commits {
		shortSHA := commit.GetID()
		if len(shortSHA) > 7 {
			shortSHA = shortSHA[:7]
		}
		commitURL := fmt.Sprintf("%s/commit/%s", repoURL, commit.GetID())
		var authorStr string
		if login := commit.Author.GetLogin(); login != "" {
			authorStr = FormatUser(login)
		} else {
			authorStr = EscapeMarkdownV2(commit.Author.GetName())
		}

		commitMessage := FormatTextWithMarkdown(commit.GetMessage())

		msg += fmt.Sprintf(
			"\\- [%s](%s): %s by %s\n",
			EscapeMarkdownV2(shortSHA),
			EscapeMarkdownV2URL(commitURL),
			commitMessage,
			authorStr,
		)
	}

	if len(msg) > 4000 {
		msg = fmt.Sprintf(
			"🔨 *%d new commit(s) to* `%s:%s`\n\n"+
				"⚠️ _Too many commits to display, check the repository for details\\._\n",
			commitCount, EscapeMarkdownV2(repo), EscapeMarkdownV2(branch),
		)
	}

	if commitCount == 1 {
		commitURL := fmt.Sprintf("%s/commit/%s", repoURL, commits[0].GetID())
		return FormatMessageWithButton(msg, "View Commit", commitURL)
	}
	return FormatMessageWithButton(msg, "View Commits", compareURL)
}

func FormatCreateEvent(event *github.CreateEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := event.Repo.GetFullName()
	repoURL := event.Repo.GetHTMLURL()
	sender := event.Sender.GetLogin()
	refType := event.GetRefType()
	ref := event.GetRef()

	msg := fmt.Sprintf(
		"✨ *New %s created*\n\n"+
			"*Name:* `%s`\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(refType),
		EscapeMarkdownV2(ref),
		FormatRepo(repo),
		FormatUser(sender),
	)

	if desc := event.GetDescription(); desc != "" {
		msg += fmt.Sprintf("*Description:* %s\n", FormatTextWithMarkdown(desc))
	}

	if refType == "repository" && event.GetMasterBranch() != "" {
		msg += fmt.Sprintf("*Default branch:* %s\n", EscapeMarkdownV2(event.GetMasterBranch()))
	}

	return FormatMessageWithButton(msg, "View Repository", repoURL)
}

func FormatDeleteEvent(event *github.DeleteEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := event.Repo.GetFullName()
	repoURL := event.Repo.GetHTMLURL()
	sender := event.Sender.GetLogin()
	refType := event.GetRefType()
	ref := event.GetRef()

	emoji := "❌"
	switch refType {
	case "branch":
		emoji = "🌿"
	case "tag":
		emoji = "🏷️"
	}

	msg := fmt.Sprintf(
		"%s *Deleted %s:* `%s`\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s",
		emoji,
		EscapeMarkdownV2(refType),
		EscapeMarkdownV2(ref),
		FormatRepo(repo),
		FormatUser(sender),
	)

	return FormatMessageWithButton(msg, "View Repository", repoURL)
}

func FormatForkEvent(event *github.ForkEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	originalRepo := event.Repo.GetFullName()
	forkedRepo := event.Forkee.GetFullName()
	sender := event.Sender.GetLogin()
	msg := fmt.Sprintf(
		"🍴 %s forked by %s\n\n"+
			"✨ *Stars:* %d \\| 🍴 *Forks:* %d",
		FormatRepo(originalRepo),
		FormatUser(sender),
		event.Repo.GetStargazersCount(),
		event.Repo.GetForksCount(),
	)

	return FormatMessageWithButton(msg, "View Fork", fmt.Sprintf("https://github.com/%s", EscapeMarkdownV2URL(forkedRepo)))
}

func FormatCommitCommentEvent(event *github.CommitCommentEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	comment := event.Comment.GetBody()
	commitSHA := event.Comment.GetCommitID()
	repo := event.Repo.GetFullName()
	sender := event.Sender.GetLogin()
	action := event.GetAction()
	commitURL := fmt.Sprintf("https://github.com/%s/commit/%s", EscapeMarkdownV2URL(repo), EscapeMarkdownV2URL(commitSHA))

	actionEmoji := map[string]string{
		"created": "💬",
		"edited":  "✏️",
		"deleted": "🗑️",
	}[action]

	if actionEmoji == "" {
		actionEmoji = "⚠️"
	}

	msg := fmt.Sprintf(
		"%s *%s %s comment on commit*\n\n"+
			"*Repository:* %s\n"+
			"*Commit:* [`%s`](%s)\n",
		actionEmoji,
		FormatUser(sender),
		EscapeMarkdownV2(action),
		FormatRepo(repo),
		EscapeMarkdownV2(commitSHA[:7]),
		commitURL,
	)

	if action == "created" || action == "edited" {
		msg += fmt.Sprintf("*Comment:* %s", FormatTextWithMarkdown(comment))
	}

	return FormatMessageWithButton(msg, "View Comment", event.Comment.GetHTMLURL())
}

func FormatPublicEvent(event *github.PublicEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := fmt.Sprintf(
		"🔓 *Repository made public*\n\n"+
			"*Name:* %s\n"+
			"*By:* %s",
		FormatRepo(event.Repo.GetFullName()),
		FormatUser(event.Sender.GetLogin()),
	)
	return FormatMessageWithButton(msg, "View Repository", event.Repo.GetHTMLURL())
}

func FormatIssueCommentEvent(event *github.IssueCommentEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := event.GetAction()
	issue := event.Issue
	comment := event.Comment
	repo := event.Repo.GetFullName()
	sender := event.Sender.GetLogin()

	actionEmoji := map[string]string{
		"created": "💬",
		"edited":  "✏️",
		"deleted": "🗑️",
	}[action]
	if actionEmoji == "" {
		actionEmoji = "⚠️"
	}

	msg := fmt.Sprintf(
		"%s *%s %s comment on* [%s\\#%d](%s)\n\n"+
			"*Title:* %s\n",
		actionEmoji,
		FormatUser(sender),
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(repo),
		issue.GetNumber(),
		EscapeMarkdownV2URL(issue.GetHTMLURL()),
		EscapeMarkdownV2(issue.GetTitle()),
	)

	if action == "created" || action == "edited" {
		msg += fmt.Sprintf("*Comment:* %s", FormatTextWithMarkdown(comment.GetBody()))
	}

	return FormatMessageWithButton(msg, "View Comment", comment.GetHTMLURL())
}

func FormatMemberEvent(event *github.MemberEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := event.GetAction()
	member := event.Member.GetLogin()
	repo := event.Repo.GetFullName()
	sender := event.Sender.GetLogin()

	actionInfo := map[string]struct {
		emoji string
		verb  string
	}{
		"added":   {"➕", "added to"},
		"removed": {"➖", "removed from"},
		"edited":  {"✏️", "updated in"},
	}[action]

	if actionInfo.emoji == "" {
		actionInfo = struct {
			emoji string
			verb  string
		}{"⚠️", "performed action on"}
	}

	msg := fmt.Sprintf(
		"%s *%s* %s *%s*\n\n"+
			"*By:* %s",
		actionInfo.emoji,
		FormatUser(member),
		EscapeMarkdownV2(actionInfo.verb),
		FormatRepo(repo),
		FormatUser(sender),
	)

	if action == "edited" && event.Changes != nil {
		msg += fmt.Sprintf("\n*Changes:* %v", event.Changes)
	}

	return FormatMessageWithButton(msg, "View Repository", event.Repo.GetHTMLURL())
}

func FormatRepositoryEvent(event *github.RepositoryEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := event.GetAction()
	repo := event.Repo.GetFullName()
	url := event.Repo.GetHTMLURL()
	sender := event.Sender.GetLogin()

	actionDetails := map[string]struct {
		emoji string
		desc  string
	}{
		"created":    {"🎉", "created"},
		"renamed":    {"🔄", fmt.Sprintf("renamed to %s", EscapeMarkdownV2(event.Repo.GetName()))},
		"archived":   {"🔒", "archived"},
		"unarchived": {"🔓", "unarchived"},
	}[action]

	if actionDetails.emoji == "" {
		actionDetails = struct {
			emoji string
			desc  string
		}{"⚠️", fmt.Sprintf("performed %s action", action)}
	}

	msg := fmt.Sprintf(
		"%s %s %s\n\n"+
			"👤 *By:* %s",
		actionDetails.emoji,
		FormatRepo(repo),
		EscapeMarkdownV2(actionDetails.desc),
		FormatUser(sender),
	)
	return FormatMessageWithButton(msg, "View Repository", url)
}

func FormatReleaseEvent(event *github.ReleaseEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := event.GetAction()
	release := event.GetRelease()
	repo := event.GetRepo().GetFullName()
	sender := event.GetSender().GetLogin()

	actionDetails := map[string]struct {
		emoji string
		verb  string
	}{
		"created":   {"🎉", "New release"},
		"published": {"🚀", "Release published"},
		"deleted":   {"🗑️", "Release deleted"},
		"edited":    {"✏️", "Release edited"},
	}[action]

	if actionDetails.emoji == "" {
		actionDetails = struct {
			emoji string
			verb  string
		}{"⚠️", fmt.Sprintf("Unknown action (%s)", action)}
	}

	msg := fmt.Sprintf(
		"%s *%s in* %s\n\n"+
			"*Tag:* %s\n"+
			"*By:* %s",
		actionDetails.emoji,
		EscapeMarkdownV2(actionDetails.verb),
		FormatRepo(repo),
		EscapeMarkdownV2(release.GetTagName()),
		FormatUser(sender),
	)

	if (action == "created" || action == "edited") && release.GetBody() != "" {
		msg += fmt.Sprintf("\n*Notes:*\n%s", FormatReleaseBody(release.GetBody()))
	}

	return FormatMessageWithButton(msg, "View Release", release.GetHTMLURL())
}

func FormatWatchEvent(event *github.WatchEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := event.GetAction()
	log.Printf("Watch action: %s", action)
	if action == "started" {
		repo := event.GetRepo()
		msg := fmt.Sprintf(
			"⭐ %s starred %s\n\n"+
				"✨ *Stars:* %d \\| 🍴 *Forks:* %d",
			FormatUser(event.GetSender().GetLogin()),
			FormatRepo(repo.GetFullName()),
			repo.GetStargazersCount(),
			repo.GetForksCount(),
		)
		return FormatMessageWithButton(msg, "View Repository", event.GetRepo().GetHTMLURL())

	}

	msg := fmt.Sprintf(
		"⚠️ *Unexpected watch action:* %s on %s by %s",
		EscapeMarkdownV2(action),
		FormatRepo(event.GetRepo().GetFullName()),
		FormatUser(event.GetSender().GetLogin()),
	)

	return msg, nil
}

func FormatStatusEvent(event *github.StatusEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	state := event.GetState()
	stateEmoji := map[string]string{
		"success": "✅",
		"error":   "❌",
		"pending": "⏳",
	}[state]
	if stateEmoji == "" {
		stateEmoji = "⚠️"
	}

	msg := fmt.Sprintf(
		"%s *%s for commit* [`%s`](%s)\n\n"+
			"*Repository:* %s\n"+
			"*Status:* %s\n"+
			"*By:* %s",
		stateEmoji,
		EscapeMarkdownV2(strings.Title(state)),
		EscapeMarkdownV2(event.GetCommit().GetSHA()[:7]),
		EscapeMarkdownV2URL(event.GetCommit().GetHTMLURL()),
		FormatRepo(event.GetRepo().GetFullName()),
		EscapeMarkdownV2(event.GetDescription()),
		FormatUser(event.GetSender().GetLogin()),
	)
	return FormatMessageWithButton(msg, "View Commit", event.GetCommit().GetHTMLURL())
}

func FormatWorkflowRunEvent(e *github.WorkflowRunEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	workflow := e.GetWorkflow().GetName()
	run := e.GetWorkflowRun()
	repo := e.GetRepo().GetFullName()
	sender := e.GetSender().GetLogin()

	var statusEmoji string
	var statusLabel string
	conclusion := run.GetConclusion()
	status := run.GetStatus()

	switch status {
	case "completed":
		switch conclusion {
		case "success":
			statusEmoji = "✅"
			statusLabel = "Success"
		case "failure":
			statusEmoji = "❌"
			statusLabel = "Failed"
		case "neutral":
			statusEmoji = "⚖️"
			statusLabel = "Neutral"
		case "cancelled":
			statusEmoji = "⛔"
			statusLabel = "Cancelled"
		default:
			statusEmoji = "🏁"
			statusLabel = "Completed"
		}
	case "in_progress":
		statusEmoji = "⏳"
		statusLabel = "Running"
	case "queued":
		statusEmoji = "🔄"
		statusLabel = "Queued"
	default:
		statusEmoji = "⚠️"
		statusLabel = "Unknown status"
	}

	msg := fmt.Sprintf(
		"%s *%s workflow*\n\n"+
			"*Status:* %s\n"+
			"*Repository:* %s\n"+
			"*By:* %s",
		statusEmoji,
		EscapeMarkdownV2(workflow),
		EscapeMarkdownV2(statusLabel),
		FormatRepo(repo),
		FormatUser(sender),
	)
	return FormatMessageWithButton(msg, "View Run", run.GetHTMLURL())
}

func FormatWorkflowJobEvent(e *github.WorkflowJobEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "⚙️ *No workflow job data*", nil
	}

	job := e.GetWorkflowJob()
	if job == nil {
		return "⚙️ *Invalid workflow job*", nil
	}

	status := job.GetStatus()
	conclusion := job.GetConclusion()
	statusEmoji := "⚙️"
	statusText := strings.Title(status)

	switch {
	case status == "completed" && conclusion == "success":
		statusEmoji = "✅"
		statusText = "Success"
	case status == "completed" && conclusion == "failure":
		statusEmoji = "❌"
		statusText = "Failed"
	case status == "in_progress":
		statusEmoji = "⏳"
	case status == "queued":
		statusEmoji = "🔄"
	case conclusion == "cancelled":
		statusEmoji = "⛔"
		statusText = "Cancelled"
	}

	msg := fmt.Sprintf("%s *Workflow Job %s*\n\n", statusEmoji, EscapeMarkdownV2(statusText))
	msg += fmt.Sprintf("*Name:* %s\n", EscapeMarkdownV2(job.GetName()))
	msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(e.GetRepo().GetFullName()))

	if !job.GetStartedAt().IsZero() {
		msg += fmt.Sprintf("*Started:* %s\n", EscapeMarkdownV2(job.GetStartedAt().Format("2006-01-02 15:04")))
	}
	if !job.GetCompletedAt().IsZero() {
		msg += fmt.Sprintf("*Completed:* %s\n", EscapeMarkdownV2(job.GetCompletedAt().Format("2006-01-02 15:04")))
	}

	if runner := job.GetRunnerName(); runner != "" {
		msg += fmt.Sprintf("*Runner:* %s\n", EscapeMarkdownV2(runner))
	}

	msg += fmt.Sprintf("*By:* %s\n", FormatUser(e.GetSender().GetLogin()))
	return FormatMessageWithButton(msg, "View Job", job.GetHTMLURL())
}

func FormatWorkflowDispatchEvent(e *github.WorkflowDispatchEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := e.GetRepo().GetFullName()
	workflow := e.GetWorkflow()
	if workflow == "" {
		workflow = "Unnamed Workflow"
	}

	inputs := "No inputs"
	if e.Inputs != nil {
		var inputsMap map[string]interface{}
		if err := json.Unmarshal(e.Inputs, &inputsMap); err == nil && len(inputsMap) > 0 {
			var inputPairs []string
			for k, v := range inputsMap {
				inputPairs = append(inputPairs, fmt.Sprintf("%s: %v", k, v))
			}
			inputs = strings.Join(inputPairs, ", ")
		}
	}

	msg := fmt.Sprintf(
		"🚀 *%s manually triggered*\n\n"+
			"*Repository:* %s\n"+
			"*Branch:* %s\n"+
			"*Inputs:* %s\n"+
			"*By:* %s",
		EscapeMarkdownV2(workflow),
		FormatRepo(repo),
		EscapeMarkdownV2(e.GetRef()),
		EscapeMarkdownV2(inputs),
		FormatUser(e.GetSender().GetLogin()),
	)
	return FormatMessageWithButton(msg, "View Repository", e.GetRepo().GetHTMLURL())
}

func FormatTeamAddEvent(e *github.TeamAddEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := fmt.Sprintf(
		"👥 *Team added*\n\n"+
			"*Team:* %s\n"+
			"*Repository:* %s\n"+
			"*Org:* %s\n"+
			"*By:* %s",
		EscapeMarkdownV2(e.GetTeam().GetName()),
		FormatRepo(e.GetRepo().GetFullName()),
		EscapeMarkdownV2(e.GetOrg().GetLogin()),
		FormatUser(e.GetSender().GetLogin()),
	)
	return FormatMessageWithButton(msg, "View Team", e.GetTeam().GetHTMLURL())
}

func FormatTeamEvent(e *github.TeamEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	team := e.GetTeam().GetName()
	org := e.GetOrg().GetLogin()
	sender := e.GetSender().GetLogin()

	actionInfo := map[string]struct {
		emoji string
		verb  string
	}{
		"created": {"🆕", "created"},
		"edited":  {"✏️", "modified"},
		"deleted": {"🗑️", "deleted"},
	}[action]

	if actionInfo.emoji == "" {
		actionInfo = struct {
			emoji string
			verb  string
		}{"⚙️", action}
	}

	msg := fmt.Sprintf(
		"%s *Team %s*\n\n"+
			"*Name:* %s\n"+
			"*Org:* %s\n"+
			"*By:* %s",
		actionInfo.emoji,
		EscapeMarkdownV2(actionInfo.verb),
		EscapeMarkdownV2(team),
		EscapeMarkdownV2(org),
		FormatUser(sender),
	)
	return FormatMessageWithButton(msg, "View Team", e.GetTeam().GetHTMLURL())
}

func FormatStarEvent(e *github.StarEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	emoji := "⭐️"
	actionText := "starred"
	if action == "deleted" {
		emoji = "❌"
		actionText = "unstarred"
	} else {
		emoji = "❌"
		action = "unknown"
	}

	user := e.GetSender().GetLogin()
	userURL := e.GetSender().GetHTMLURL()
	repo := e.GetRepo().GetFullName()
	repoURL := e.GetRepo().GetHTMLURL()
	stars := e.GetRepo().GetStargazersCount()
	forks := e.GetRepo().GetForksCount()

	msg := fmt.Sprintf(
		"%s %s (%s) %s %s (%s)\n\n✨ Stars: %d | 🍴 Forks: %d",
		emoji,
		EscapeMarkdownV2(user),
		EscapeMarkdownV2(userURL),
		EscapeMarkdownV2(actionText),
		EscapeMarkdownV2(repo),
		EscapeMarkdownV2(repoURL),
		stars,
		forks,
	)

	return FormatMessageWithButton(msg, "View Repository", repoURL)
}

func FormatRepositoryDispatchEvent(e *github.RepositoryDispatchEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := e.GetRepo().GetFullName()
	sender := e.GetSender().GetLogin()
	action := e.GetAction()
	branch := e.Branch
	if branch == nil {
		branch = e.Repo.MasterBranch
	}

	var payloadStr string
	if e.ClientPayload != nil {
		var payload map[string]interface{}
		if err := json.Unmarshal(e.ClientPayload, &payload); err == nil {
			if len(payload) > 0 {
				payloadBytes, _ := json.Marshal(payload)
				payloadStr = fmt.Sprintf("\n*Payload:* `%s`", EscapeMarkdownV2(string(payloadBytes)))
			}
		}
	}

	msg := fmt.Sprintf(
		"🚀 *Repository Dispatch*\n\n"+
			"*Repository:* %s\n"+
			"*Action:* %s\n"+
			"*Branch:* %s\n"+
			"*By:* %s%s",
		FormatRepo(repo),
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(branchOrDefault(branch)),
		FormatUser(sender),
		payloadStr,
	)
	return FormatMessageWithButton(msg, "View Repository", e.GetRepo().GetHTMLURL())
}

// Helper function to handle branch field
func branchOrDefault(branch *string) string {
	if branch != nil {
		return *branch
	}
	return "default branch"
}

func FormatPullRequestReviewCommentEvent(e *github.PullRequestReviewCommentEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo().GetFullName()
	comment := e.GetComment()
	pr := e.GetPullRequest()

	actionEmoji := map[string]string{
		"created": "💬",
		"edited":  "✏️",
		"deleted": "🗑️",
	}[action]
	if actionEmoji == "" {
		actionEmoji = "⚠️"
	}

	msg := fmt.Sprintf(
		"%s *PR Review Comment %s*\n\n"+
			"*Repository:* %s\n"+
			"*PR:* [%s\\#%d](%s)\n"+
			"*Comment:* %s\n",
		actionEmoji,
		EscapeMarkdownV2(action),
		FormatRepo(repo),
		EscapeMarkdownV2(pr.GetTitle()),
		pr.GetNumber(),
		EscapeMarkdownV2URL(pr.GetHTMLURL()),
		FormatTextWithMarkdown(comment.GetBody()),
	)
	return FormatMessageWithButton(msg, "View Comment", comment.GetHTMLURL())
}

func FormatPullRequestReviewEvent(e *github.PullRequestReviewEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	review := e.GetReview()
	pr := e.GetPullRequest()

	stateEmoji := map[string]string{
		"approved":          "✅",
		"changes_requested": "✏️",
		"commented":         "💬",
		"dismissed":         "❌",
	}[review.GetState()]

	if stateEmoji == "" {
		stateEmoji = "🔍"
	}

	msg := fmt.Sprintf(
		"%s *PR Review %s*\n\n"+
			"*Repository:* %s\n"+
			"*PR:* [%s\\#%d](%s)\n"+
			"*State:* %s\n"+
			"*By:* %s\n",
		stateEmoji,
		EscapeMarkdownV2(action),
		FormatRepo(e.GetRepo().GetFullName()),
		EscapeMarkdownV2(pr.GetTitle()),
		pr.GetNumber(),
		EscapeMarkdownV2URL(pr.GetHTMLURL()),
		EscapeMarkdownV2(review.GetState()),
		FormatUser(e.GetSender().GetLogin()),
	)
	return FormatMessageWithButton(msg, "View Review", review.GetHTMLURL())
}

func FormatPingEvent(e *github.PingEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "🏓 *Webhook Ping Received*\n\n"

	if e.Zen != nil {
		msg += fmt.Sprintf("🧘 _%s_\n", EscapeMarkdownV2(*e.Zen))
	}

	if e.Repo != nil {
		msg += fmt.Sprintf(
			"📦 %s\n",
			FormatRepo(*e.Repo.FullName),
		)
	}

	if e.Sender != nil {
		msg += fmt.Sprintf("👤 *By:* %s\n", FormatUser(*e.Sender.Login))
	}

	if e.Org != nil {
		msg += fmt.Sprintf("🏢 *Org:* %s", EscapeMarkdownV2(*e.Org.Login))
	}

	return msg, nil
}

func FormatSponsorshipEvent(e *github.SponsorshipEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender()
	sponsorship := e.GetChanges()

	msg := fmt.Sprintf(
		"💖 *Sponsorship %s*\n\n"+
			"*Sponsor:* %s\n",
		EscapeMarkdownV2(action),
		FormatUser(sender.GetLogin()),
	)
	if sponsorship != nil && sponsorship.Tier != nil {
		msg += fmt.Sprintf("*Tier:* `%s` -> `%s`\n", sponsorship.Tier.GetFrom(), "new_tier")
	}

	return FormatMessageWithButton(msg, "View Sponsorship", sender.GetHTMLURL())
}

func FormatUserEvent(e *github.UserEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	user := e.GetUser()

	msg := fmt.Sprintf(
		"👤 *User %s*\n\n"+
			"*User:* %s\n",
		EscapeMarkdownV2(action),
		FormatUser(user.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View User", user.GetHTMLURL())
}

func FormatRepositoryImportEvent(e *github.RepositoryImportEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	status := e.GetStatus()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"📥 *Repository Import %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(status),
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Repository", repo.GetHTMLURL())
}

func FormatRepositoryRulesetEvent(e *github.RepositoryRulesetEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepository()
	sender := e.GetSender()
	ruleset := e.RepositoryRuleset

	msg := fmt.Sprintf(
		"📜 *Repository Ruleset %s*\n\n"+
			"*Repository:* %s\n"+
			"*Ruleset:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(ruleset.Name),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Ruleset", fmt.Sprintf("%s/settings/rules/%d", repo.GetHTMLURL(), ruleset.GetID()))
}

func FormatSecretScanningAlertEvent(e *github.SecretScanningAlertEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	alert := e.GetAlert()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🤫 *Secret Scanning Alert %s*\n\n"+
			"*Repository:* %s\n"+
			"*Secret Type:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(alert.GetSecretType()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Alert", alert.GetHTMLURL())
}

func FormatSecretScanningAlertLocationEvent(e *github.SecretScanningAlertLocationEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"📍 *Secret Scanning Alert Location %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Alert", e.GetAlert().GetHTMLURL())
}

func FormatSecurityAndAnalysisEvent(e *github.SecurityAndAnalysisEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := e.GetRepository()
	sender := e.GetSender()
	changes := e.Changes

	var fromStatus string
	if changes.From != nil && changes.From.SecurityAndAnalysis != nil && changes.From.GetSecurityAndAnalysis() != nil && changes.From.GetSecurityAndAnalysis().AdvancedSecurity != nil {
		fromStatus = changes.From.GetSecurityAndAnalysis().AdvancedSecurity.GetStatus()
	}

	msg := fmt.Sprintf(
		"🔒 *Security & Analysis Settings Updated*\n\n"+
			"*Repository:* %s\n"+
			"*From Status:* `%s`\n"+
			"*By:* %s\n",
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(fromStatus),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Security Settings", fmt.Sprintf("%s/settings/security_analysis", repo.GetHTMLURL()))
}

func FormatPullRequestReviewThreadEvent(e *github.PullRequestReviewThreadEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()
	pr := e.GetPullRequest()

	msg := fmt.Sprintf(
		"🧵 *PR Review Thread %s*\n\n"+
			"*Repository:* %s\n"+
			"*Pull Request:* [%s](%s)\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(pr.GetTitle()),
		EscapeMarkdownV2URL(pr.GetHTMLURL()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Thread", e.GetThread().Comments[0].GetHTMLURL())
}

func FormatPullRequestTargetEvent(e *github.PullRequestTargetEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()
	pr := e.GetPullRequest()

	msg := fmt.Sprintf(
		"🎯 *PR Target %s*\n\n"+
			"*Repository:* %s\n"+
			"*Pull Request:* [%s](%s)\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(pr.GetTitle()),
		EscapeMarkdownV2URL(pr.GetHTMLURL()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View PR", pr.GetHTMLURL())
}

func FormatRegistryPackageEvent(e *github.RegistryPackageEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepository()
	sender := e.GetSender()
	pkg := e.RegistryPackage

	msg := fmt.Sprintf(
		"📦 *Registry Package %s*\n\n"+
			"*Repository:* %s\n"+
			"*Package:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(pkg.GetName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Package", pkg.GetHTMLURL())
}

func FormatMergeGroupEvent(e *github.MergeGroupEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🔄 *Merge Group %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Repository", repo.GetHTMLURL())
}

func FormatPersonalAccessTokenRequestEvent(e *github.PersonalAccessTokenRequestEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	org := e.GetOrg()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🔑 *Personal Access Token Request %s*\n\n"+
			"*Organization:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(org.GetLogin()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Organization Settings", fmt.Sprintf("https://github.com/organizations/%s/settings/personal-access-tokens", org.GetLogin()))
}

func FormatProjectV2Event(e *github.ProjectV2Event) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	org := e.GetOrg()
	sender := e.GetSender()
	project := e.ProjectsV2

	msg := fmt.Sprintf(
		"📋 *Project %s*\n\n"+
			"*Organization:* %s\n"+
			"*Project:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(org.GetLogin()),
		EscapeMarkdownV2(project.GetTitle()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Project", project.GetHTMLURL())
}

func FormatProjectV2ItemEvent(e *github.ProjectV2ItemEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	org := e.GetOrg()
	sender := e.GetSender()
	item := e.ProjectV2Item

	msg := fmt.Sprintf(
		"📄 *Project Item %s*\n\n"+
			"*Organization:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(org.GetLogin()),
		FormatUser(sender.GetLogin()),
	)
	contentType := item.GetContentType()
	if contentType != nil && *contentType == github.ProjectV2ItemContentTypePullRequest {
		msg += fmt.Sprintf("*Pull Request:* %s\n", item.GetContentNodeID())
	} else if contentType != nil && *contentType == github.ProjectV2ItemContentTypeIssue {
		msg += fmt.Sprintf("*Issue:* %s\n", item.GetContentNodeID())
	} else if contentType != nil && *contentType == github.ProjectV2ItemContentTypeDraftIssue {
		msg += fmt.Sprintf("*Draft Issue:* %s\n", item.GetContentNodeID())
	}

	return FormatMessageWithButton(msg, "View Item", item.GetProjectURL())
}

func FormatGitHubAppAuthorizationEvent(e *github.GitHubAppAuthorizationEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🔒 *GitHub App Authorization %s*\n\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatUser(sender.GetLogin()),
	)

	return msg, nil
}

func FormatInstallationRepositoriesEvent(e *github.InstallationRepositoriesEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender()
	reposAdded := e.RepositoriesAdded
	reposRemoved := e.RepositoriesRemoved

	msg := fmt.Sprintf(
		"📦 *Installation Repositories %s*\n\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatUser(sender.GetLogin()),
	)
	if len(reposAdded) > 0 {
		var repoNames []string
		for _, r := range reposAdded {
			repoNames = append(repoNames, FormatRepo(r.GetFullName()))
		}
		msg += fmt.Sprintf("*Repositories Added:*\n%s\n", strings.Join(repoNames, "\n"))
	}
	if len(reposRemoved) > 0 {
		var repoNames []string
		for _, r := range reposRemoved {
			repoNames = append(repoNames, FormatRepo(r.GetFullName()))
		}
		msg += fmt.Sprintf("*Repositories Removed:*\n%s\n", strings.Join(repoNames, "\n"))
	}

	return FormatMessageWithButton(msg, "View Installation", e.GetInstallation().GetHTMLURL())
}

func FormatInstallationTargetEvent(e *github.InstallationTargetEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender()
	target := e.GetAccount()

	msg := fmt.Sprintf(
		"🎯 *Installation Target %s*\n\n"+
			"*Target:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatUser(target.GetLogin()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Installation", e.GetInstallation().GetHTMLURL())
}

func FormatDiscussionCommentEvent(e *github.DiscussionCommentEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()
	discussion := e.GetDiscussion()
	comment := e.GetComment()

	msg := fmt.Sprintf(
		"💬 *Discussion Comment %s*\n\n"+
			"*Repository:* %s\n"+
			"*Discussion:* [%s](%s)\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(discussion.GetTitle()),
		EscapeMarkdownV2URL(discussion.GetHTMLURL()),
		FormatUser(sender.GetLogin()),
	)
	if action != "deleted" {
		msg += fmt.Sprintf("*Comment:* %s\n", FormatTextWithMarkdown(comment.GetBody()))
	}

	return FormatMessageWithButton(msg, "View Comment", comment.GetHTMLURL())
}

func FormatDiscussionEvent(e *github.DiscussionEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()
	discussion := e.GetDiscussion()

	msg := fmt.Sprintf(
		"📣 *Discussion %s*\n\n"+
			"*Repository:* %s\n"+
			"*Title:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(discussion.GetTitle()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Discussion", discussion.GetHTMLURL())
}

func FormatDependabotAlertEvent(e *github.DependabotAlertEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	alert := e.GetAlert()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🤖 *Dependabot Alert %s*\n\n"+
			"*Repository:* %s\n"+
			"*Package:* `%s`\n"+
			"*Severity:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(alert.GetSecurityVulnerability().Package.GetName()),
		EscapeMarkdownV2(alert.GetSecurityVulnerability().GetSeverity()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Alert", alert.GetHTMLURL())
}

func FormatDeploymentProtectionRuleEvent(e *github.DeploymentProtectionRuleEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🛡️ *Deployment Protection Rule %s*\n\n"+
			"*Repository:* %s\n"+
			"*Environment:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(e.GetEnvironment()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Deployment", e.GetDeployment().GetURL())
}

func FormatDeploymentReviewEvent(e *github.DeploymentReviewEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🔎 *Deployment Review %s*\n\n"+
			"*Repository:* %s\n"+
			"*Environment:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(e.GetEnvironment()),
		FormatUser(sender.GetLogin()),
	)
	if e.Comment != nil {
		msg += fmt.Sprintf("*Comment:* %s\n", EscapeMarkdownV2(e.GetComment()))
	}

	return FormatMessageWithButton(msg, "View Workflow Run", e.GetWorkflowRun().GetHTMLURL())
}

func FormatContentReferenceEvent(e *github.ContentReferenceEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.GetRepo()
	sender := e.GetSender()
	ref := e.GetContentReference()

	msg := fmt.Sprintf(
		"🔗 *Content Reference %s*\n\n"+
			"*Repository:* %s\n"+
			"*Reference:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(ref.GetReference()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Repository", repo.GetHTMLURL())
}

func FormatCustomPropertyEvent(e *github.CustomPropertyEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	org := e.GetOrg()
	sender := e.GetSender()
	prop := e.Definition

	msg := fmt.Sprintf(
		"📝 *Custom Property %s*\n\n"+
			"*Organization:* %s\n"+
			"*Property Name:* `%s`\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		EscapeMarkdownV2(org.GetLogin()),
		EscapeMarkdownV2(prop.GetPropertyName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Organization Settings", fmt.Sprintf("https://github.com/organizations/%s/settings/custom-properties", org.GetLogin()))
}

func FormatCustomPropertyValuesEvent(e *github.CustomPropertyValuesEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	repo := e.GetRepo()
	sender := e.GetSender()

	var props []string
	for _, p := range e.NewPropertyValues {
		props = append(props, fmt.Sprintf("`%s`: `%s`", p.PropertyName, p.Value))
	}

	msg := fmt.Sprintf(
		"🔄 *Custom Property Values Updated*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n"+
			"*New Values:*\n%s",
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
		strings.Join(props, "\n"),
	)

	return FormatMessageWithButton(msg, "View Repository Settings", fmt.Sprintf("%s/settings/custom-properties", repo.GetHTMLURL()))
}

func FormatBranchProtectionRuleEvent(e *github.BranchProtectionRuleEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.Repo
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🛡️ *Branch Protection Rule %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
	)
	if e.Rule != nil {
		msg += fmt.Sprintf("*Rule Name:* %s\n", EscapeMarkdownV2(e.Rule.GetName()))
	}

	return FormatMessageWithButton(msg, "View Branch Settings", fmt.Sprintf("%s/settings/branches", repo.GetHTMLURL()))
}

func FormatBranchProtectionConfigurationEvent(e *github.BranchProtectionConfigurationEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	repo := e.Repo
	sender := e.GetSender()

	msg := fmt.Sprintf(
		"🛡️ *Branch Protection Configuration %s*\n\n"+
			"*Repository:* %s\n"+
			"*By:* %s\n",
		EscapeMarkdownV2(action),
		FormatRepo(repo.GetFullName()),
		FormatUser(sender.GetLogin()),
	)

	return FormatMessageWithButton(msg, "View Repository", repo.GetHTMLURL())
}

func FormatRepositoryVulnerabilityAlertEvent(e *github.RepositoryVulnerabilityAlertEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	alert := e.GetAlert()
	repo := e.Repository

	msg := fmt.Sprintf(
		"🚨 *Vulnerability Alert: %s*\n\n"+
			"*Repository:* %s\n"+
			"*Severity:* %s\n"+
			"*Package:* %s\n",
		EscapeMarkdownV2(alert.GetAffectedPackageName()),
		FormatRepo(repo.GetFullName()),
		EscapeMarkdownV2(alert.GetSeverity()),
		EscapeMarkdownV2(alert.GetAffectedPackageName()),
	)

	return FormatMessageWithButton(msg, "View Alert", fmt.Sprintf("%s/security/advisories/%s", repo.GetHTMLURL(), alert.GetGitHubSecurityAdvisoryID()))
}

func FormatPageBuildEvent(e *github.PageBuildEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "🏗️ *GitHub Pages Build*\n\n"

	if e.Build != nil {
		status := "unknown"
		if e.Build.Status != nil {
			status = *e.Build.Status
		}
		msg += fmt.Sprintf("*Status:* %s\n", EscapeMarkdownV2(status))

		if e.Build.Error != nil {
			msg += fmt.Sprintf("*Error:* %v\n", EscapeMarkdownV2(fmt.Sprintf("%v", *e.Build.Error)))
		}
	}

	if e.Repo != nil {
		msg += fmt.Sprintf(
			"📦 %s\n",
			FormatRepo(*e.Repo.FullName),
		)
	}

	if e.Sender != nil {
		msg += fmt.Sprintf("👤 *By:* %s", FormatUser(*e.Sender.Login))
	}

	return FormatMessageWithButton(msg, "View Repository", e.GetRepo().GetHTMLURL())
}

func FormatPackageEvent(e *github.PackageEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "📦 *Package Event*\n\n"

	if e.Package != nil && e.Package.Name != nil {
		msg += fmt.Sprintf("*Package:* %s\n", EscapeMarkdownV2(*e.Package.Name))
	}

	if e.Repo != nil && e.Repo.Name != nil {
		msg += fmt.Sprintf(
			"*Repository:* %s\n",
			FormatRepo(*e.Repo.FullName),
		)
	}

	if e.Sender != nil && e.Sender.Login != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(*e.Sender.Login))
	}

	return FormatMessageWithButton(msg, "View Package", e.GetPackage().GetHTMLURL())
}

func FormatOrgBlockEvent(e *github.OrgBlockEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "🚫 *Organization Block*\n\n"
	if user := e.GetBlockedUser(); user != nil {
		msg += fmt.Sprintf("*Blocked:* %s\n", FormatUser(user.GetLogin()))
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Organization", e.GetOrganization().GetHTMLURL())
}

func FormatOrganizationEvent(e *github.OrganizationEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender()

	msg := fmt.Sprintf("🏢 *Organization Event*\n*Action:* %s", EscapeMarkdownV2(action))

	if sender != nil {
		msg += fmt.Sprintf("\n*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Organization", e.GetOrganization().GetHTMLURL())
}

func FormatMilestoneEvent(e *github.MilestoneEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	milestone := e.GetMilestone()
	action := e.GetAction()

	msg := fmt.Sprintf("🏁 *Milestone %s*\n\n", EscapeMarkdownV2(action))

	if milestone != nil {
		msg += fmt.Sprintf("*Title:* %s\n", EscapeMarkdownV2(milestone.GetTitle()))
		if desc := milestone.GetDescription(); desc != "" {
			msg += fmt.Sprintf("*Description:* %s\n", FormatTextWithMarkdown(desc))
		}
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Milestone", e.GetMilestone().GetHTMLURL())
}

func FormatMetaEvent(e *github.MetaEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "⚙️ *Meta Event*\n\n"

	if id := e.GetHookID(); id != 0 {
		msg += fmt.Sprintf("*Hook ID:* %d\n", id)
	}

	if repo := e.GetRepo(); repo != nil {
		msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(repo.GetFullName()))
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s\n", FormatUser(sender.GetLogin()))
	}

	if org := e.GetOrg(); org != nil {
		msg += fmt.Sprintf("*Org:* %s\n", EscapeMarkdownV2(org.GetLogin()))
	}

	if install := e.GetInstallation(); install != nil {
		msg += fmt.Sprintf("*Install ID:* %d", install.GetID())
	}

	return msg, nil
}

func FormatMembershipEvent(e *github.MembershipEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "🚫 *No membership event data*", nil
	}

	msg := fmt.Sprintf("👥 *Membership %s*\n\n", EscapeMarkdownV2(e.GetAction()))

	if scope := e.GetScope(); scope != "" {
		msg += fmt.Sprintf("*Scope:* %s\n", EscapeMarkdownV2(scope))
	}

	if member := e.GetMember(); member != nil {
		msg += fmt.Sprintf("*Member:* %s\n", FormatUser(member.GetLogin()))
	}

	if team := e.GetTeam(); team != nil {
		msg += fmt.Sprintf("*Team:* %s\n", EscapeMarkdownV2(team.GetName()))
		if desc := team.GetDescription(); desc != "" {
			msg += fmt.Sprintf("*Description:* %s\n", FormatTextWithMarkdown(desc))
		}
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Team", e.GetTeam().GetHTMLURL())
}

func FormatDeploymentEvent(e *github.DeploymentEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	msg := "🚀 *Deployment Event*\n\n"

	if deploy := e.GetDeployment(); deploy != nil {
		msg += fmt.Sprintf("*ID:* %d\n", deploy.GetID())
		if desc := deploy.GetDescription(); desc != "" {
			msg += fmt.Sprintf("*Description:* %s\n", FormatTextWithMarkdown(desc))
		}
	}

	if repo := e.GetRepo(); repo != nil {
		msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(repo.GetName()))
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Deployment", e.GetDeployment().GetURL())
}

func FormatLabelEvent(e *github.LabelEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "🏷️ *No label event data*", nil
	}

	msg := fmt.Sprintf("🏷️ *Label %s*\n\n", EscapeMarkdownV2(e.GetAction()))

	if label := e.GetLabel(); label != nil {
		msg += fmt.Sprintf("*Name:* %s\n", EscapeMarkdownV2(label.GetName()))
		msg += fmt.Sprintf("*Color:* `#%s`\n", EscapeMarkdownV2(label.GetColor()))
		if desc := label.GetDescription(); desc != "" {
			msg += fmt.Sprintf("*Description:* %s\n", FormatTextWithMarkdown(desc))
		}
	}

	if changes := e.GetChanges(); changes != nil {
		if title := changes.GetTitle(); title != nil && title.GetFrom() != "" {
			msg += fmt.Sprintf("*Previous Name:* %s\n", EscapeMarkdownV2(title.GetFrom()))
		}
		if body := changes.GetBody(); body != nil && body.GetFrom() != "" {
			msg += fmt.Sprintf("*Previous Desc:* %s\n", FormatTextWithMarkdown(body.GetFrom()))
		}
	}

	return FormatMessageWithButton(msg, "View Repository", e.GetRepo().GetHTMLURL())
}

func FormatMarketplacePurchaseEvent(e *github.MarketplacePurchaseEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "🛒 *No marketplace data*", nil
	}

	msg := fmt.Sprintf("🛒 *Marketplace %s*\n\n", EscapeMarkdownV2(e.GetAction()))

	if purchase := e.GetMarketplacePurchase(); purchase != nil {
		if plan := purchase.GetPlan(); plan != nil {
			msg += fmt.Sprintf("*Plan:* %s\n", EscapeMarkdownV2(plan.GetName()))
		}
		msg += fmt.Sprintf("*Billing:* %s\n", EscapeMarkdownV2(purchase.GetBillingCycle()))
		msg += fmt.Sprintf("*Units:* %d\n", purchase.GetUnitCount())
		if nextBill := purchase.GetNextBillingDate(); !nextBill.IsZero() {
			msg += fmt.Sprintf("*Next Bill:* %s\n", EscapeMarkdownV2(nextBill.Format("2006-01-02")))
		}

		if account := purchase.GetAccount(); account != nil {
			msg += fmt.Sprintf("*Account:* %s (%s)\n",
				FormatUser(account.GetLogin()),
				EscapeMarkdownV2(account.GetType()))
		}
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return msg, nil
}

func FormatGollumEvent(e *github.GollumEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "📚 *No wiki update data available*", nil
	}

	var msg strings.Builder
	msg.WriteString("📚 *Wiki Update*\n\n")
	if repo := e.GetRepo(); repo != nil {
		msg.WriteString(fmt.Sprintf("*Repository:* %s\n",
			FormatRepo(repo.GetFullName())))
	}

	if org := e.GetOrg(); org != nil {
		msg.WriteString(fmt.Sprintf("*Organization:* %s\n", EscapeMarkdownV2(org.GetLogin())))
	}

	if sender := e.GetSender(); sender != nil {
		msg.WriteString(fmt.Sprintf("*Edited by:* %s\n", FormatUser(sender.GetLogin())))
	}

	if e.Pages != nil && len(e.Pages) > 0 {
		msg.WriteString("\n*Page Changes:*\n")
		for _, page := range e.Pages {
			if page == nil {
				continue
			}
			action := "unknown"
			if page.Action != nil {
				action = *page.Action
			}
			emoji := getActionEmoji(action)
			pageTitle := ""
			if page.Title != nil {
				pageTitle = *page.Title
			} else if page.PageName != nil {
				pageTitle = *page.PageName
			}

			if pageTitle != "" {
				msg.WriteString(fmt.Sprintf("%s *%s* (%s)\n",
					emoji,
					EscapeMarkdownV2(pageTitle),
					EscapeMarkdownV2(action)))
			}
			if page.Summary != nil && *page.Summary != "" {
				msg.WriteString(fmt.Sprintf("_Summary:_ %s\n", FormatTextWithMarkdown(*page.Summary)))
			}

			if page.SHA != nil && *page.SHA != "" {
				msg.WriteString(fmt.Sprintf("_Revision:_ %s\n", EscapeMarkdownV2((*page.SHA)[:7])))
			}
			if page.HTMLURL != nil && *page.HTMLURL != "" {
				msg.WriteString(fmt.Sprintf("[View Page](%s)\n", EscapeMarkdownV2URL(*page.HTMLURL)))
			}

			msg.WriteString("\n")
		}
	}

	return msg.String(), nil
}

func getActionEmoji(action string) string {
	switch action {
	case "created":
		return "🆕"
	case "edited":
		return "✏️"
	case "deleted":
		return "🗑️"
	default:
		return "🔹"
	}
}

func FormatDeployKeyEvent(e *github.DeployKeyEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "🔑 *No deploy key data*", nil
	}

	msg := fmt.Sprintf("🔑 *Deploy Key %s*\n\n", EscapeMarkdownV2(e.GetAction()))

	if key := e.GetKey(); key != nil {
		msg += fmt.Sprintf("*Title:* %s\n", EscapeMarkdownV2(key.GetTitle()))
		if url := key.GetURL(); url != "" {
			msg += fmt.Sprintf("[View Key](%s)\n", EscapeMarkdownV2URL(url))
		}
	}

	msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(e.GetRepo().GetName()))

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Repository", e.GetRepo().GetHTMLURL())
}

func FormatCheckSuiteEvent(e *github.CheckSuiteEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "✅ *No check suite data*", nil
	}

	suite := e.GetCheckSuite()
	var msg strings.Builder

	action := strings.Title(e.GetAction())
	msg.WriteString(fmt.Sprintf("✅ *Check Suite: %s*\n\n", EscapeMarkdownV2(action)))

	if suite != nil {
		status := suite.GetStatus()
		msg.WriteString(fmt.Sprintf("• *Status:* %s\n", EscapeMarkdownV2(status)))

		if conclusion := suite.GetConclusion(); conclusion != "" {
			msg.WriteString(fmt.Sprintf("• *Result:* %s\n", EscapeMarkdownV2(conclusion)))
		}
	}

	msg.WriteString(fmt.Sprintf("\n*Repository:* %s\n", FormatRepo(e.GetRepo().GetFullName())))

	if sender := e.GetSender(); sender != nil {
		username := sender.GetLogin()
		msg.WriteString(fmt.Sprintf("*Triggered by:* %s", EscapeMarkdownV2(username)))
	}

	return FormatMessageWithButton(msg.String(), "View Details", e.GetCheckSuite().GetURL())
}

func FormatCheckRunEvent(e *github.CheckRunEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "⚙️ *No check run data*", nil
	}

	check := e.GetCheckRun()
	var msg strings.Builder

	action := strings.Title(e.GetAction())
	msg.WriteString(fmt.Sprintf("⚙️ *Check Run: %s*\n\n", EscapeMarkdownV2(action)))

	if check != nil {
		name := check.GetName()
		status := check.GetStatus()
		msg.WriteString(fmt.Sprintf("• *Name:* %s\n", EscapeMarkdownV2(name)))
		msg.WriteString(fmt.Sprintf("• *Status:* %s\n", EscapeMarkdownV2(status)))

		if conclusion := check.GetConclusion(); conclusion != "" {
			msg.WriteString(fmt.Sprintf("• *Result:* %s\n", EscapeMarkdownV2(conclusion)))
		}

		if !check.GetStartedAt().IsZero() {
			msg.WriteString(fmt.Sprintf("• *Started:* %s\n", EscapeMarkdownV2(check.GetStartedAt().Format("2006-01-02 15:04"))))
		}

		if !check.GetCompletedAt().IsZero() {
			msg.WriteString(fmt.Sprintf("• *Completed:* %s\n", EscapeMarkdownV2(check.GetCompletedAt().Format("2006-01-02 15:04"))))
		}
	}

	msg.WriteString(fmt.Sprintf("\n*Repository:* %s\n", FormatRepo(e.GetRepo().GetFullName())))

	if sender := e.GetSender(); sender != nil {
		username := sender.GetLogin()
		msg.WriteString(fmt.Sprintf("*Triggered by:* %s", EscapeMarkdownV2(username)))
	}

	return FormatMessageWithButton(msg.String(), "View Details", e.GetCheckRun().GetHTMLURL())
}

func FormatDeploymentStatusEvent(e *github.DeploymentStatusEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "🚦 *No deployment status data*", nil
	}

	status := e.GetDeploymentStatus()
	msg := fmt.Sprintf("🚦 *Deployment %s*\n\n", EscapeMarkdownV2(status.GetState()))

	if desc := status.GetDescription(); desc != "" {
		msg += fmt.Sprintf("*Status:* %s\n", FormatTextWithMarkdown(desc))
	}

	msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(e.GetRepo().GetName()))

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Deployment", e.GetDeploymentStatus().GetDeploymentURL())
}

func FormatSecurityAdvisoryEvent(e *github.SecurityAdvisoryEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	if e == nil {
		return "⚠️ *No security advisory data*", nil
	}

	adv := e.GetSecurityAdvisory()
	msg := fmt.Sprintf("⚠️ *Security Advisory %s*\n\n", EscapeMarkdownV2(e.GetAction()))

	if adv != nil {
		msg += fmt.Sprintf("*Summary:* %s\n", FormatTextWithMarkdown(adv.GetSummary()))
		if sev := adv.GetSeverity(); sev != "" {
			msg += fmt.Sprintf("*Severity:* %s\n", EscapeMarkdownV2(sev))
		}
		if cve := adv.GetCVEID(); cve != "" {
			msg += fmt.Sprintf("*CVE:* %s\n", EscapeMarkdownV2(cve))
		}
		if url := adv.GetURL(); url != "" {
			msg += fmt.Sprintf("[View Advisory](%s)\n", EscapeMarkdownV2URL(url))
		}
		if author := adv.GetAuthor(); author != nil {
			msg += fmt.Sprintf("*Reported by:* %s\n", FormatUser(author.GetLogin()))
		}
	}

	if repo := e.GetRepository(); repo != nil {
		msg += fmt.Sprintf("*Repository:* %s\n", FormatRepo(repo.GetFullName()))
	}

	if org := e.GetOrganization(); org != nil {
		msg += fmt.Sprintf("*Org:* %s\n", EscapeMarkdownV2(org.GetLogin()))
	}

	if sender := e.GetSender(); sender != nil {
		msg += fmt.Sprintf("*By:* %s", FormatUser(sender.GetLogin()))
	}

	return FormatMessageWithButton(msg, "View Advisory", e.GetSecurityAdvisory().GetHTMLURL())
}

func FormatInstallationEvent(e *github.InstallationEvent) (string, *gotgbot.InlineKeyboardMarkup) {
	action := e.GetAction()
	sender := e.GetSender().GetLogin()

	var msg string
	switch action {
	case "created":
		msg = "🎉 *New installation*\\! Welcome aboard\\! 🎉\n\n"
		msg += "This bot will now post updates from the repositories you've granted access to\\.\n\n"
		msg += fmt.Sprintf("Installation by %s\\.", FormatUser(sender))
	case "deleted":
		msg = "🗑️ *Installation uninstalled*\\! Goodbye\\! 👋\n\n"
		msg += "This bot will no longer post updates\\.\n\n"
		msg += fmt.Sprintf("Uninstalled by %s\\.", FormatUser(sender))
	default:
		msg = fmt.Sprintf("🤖 *Unknown installation action:* `%s`", EscapeMarkdownV2(action))
	}

	return msg, nil
}
