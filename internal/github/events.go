package github

type Event struct {
	Name  string
	Label string
	Short string
}

var SupportedEvents = []Event{
	{Name: "push", Label: "Code", Short: "p"},
	{Name: "issues", Label: "Issues", Short: "i"},
	{Name: "pull_request", Label: "Pull requests", Short: "pr"},
	{Name: "gollum", Label: "Wikis", Short: "g"},
	{Name: "repository", Label: "Settings", Short: "rep"},
	{Name: "meta", Label: "Webhooks and services", Short: "mt"},
	{Name: "deploy_key", Label: "Deploy keys", Short: "dk"},
	{Name: "member", Label: "Collaboration invites", Short: "m"},
	{Name: "fork", Label: "Forks", Short: "f"},
	{Name: "star", Label: "Stars", Short: "s"},
}
