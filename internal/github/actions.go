package github

import (
	"strings"

	"github.com/google/go-github/v92/github"
)

// Unknown actions receive a neutral fallback; never infer a completed operation.
var knownActions = map[string]string{
	"issues":                      "opened edited deleted transferred pinned unpinned closed reopened assigned unassigned labeled unlabeled locked unlocked milestoned demilestoned typed untyped",
	"pull_request":                "opened edited closed reopened assigned unassigned labeled unlabeled synchronize review_requested review_request_removed ready_for_review converted_to_draft locked unlocked auto_merge_enabled auto_merge_disabled enqueued dequeued milestoned demilestoned",
	"pull_request_target":         "opened edited closed reopened assigned unassigned labeled unlabeled synchronize review_requested review_request_removed ready_for_review converted_to_draft locked unlocked auto_merge_enabled auto_merge_disabled enqueued dequeued milestoned demilestoned",
	"issue_comment":               "created edited deleted",
	"pull_request_review":         "submitted edited dismissed",
	"pull_request_review_comment": "created edited deleted",
	"pull_request_review_thread":  "resolved unresolved",
	"discussion":                  "created edited deleted pinned unpinned locked unlocked transferred category_changed answered unanswered labeled unlabeled closed reopened",
	"discussion_comment":          "created edited deleted",
	"release":                     "published unpublished created edited deleted prereleased released",
	"star":                        "created deleted",
	"watch":                       "started",
	"workflow_run":                "requested in_progress completed",
	"workflow_job":                "queued waiting in_progress completed",
	"check_run":                   "created completed rerequested requested_action",
	"check_suite":                 "completed requested rerequested",
	"repository":                  "created deleted archived unarchived edited renamed transferred publicized privatized disabled enabled",
	"member":                      "added edited deleted",
	"deploy_key":                  "created deleted",
	"meta":                        "deleted",
}

func knownAction(name, action string) bool {
	actions, ok := knownActions[name]
	return !ok || action == "" || strings.Contains(" "+actions+" ", " "+action+" ")
}

// Use published to avoid duplicate release announcements; keep draft creation.
func redundantReleaseAction(event *github.ReleaseEvent) bool {
	switch event.GetAction() {
	case "created":
		return event.GetRelease() != nil && !event.GetRelease().GetDraft()
	case "prereleased", "released":
		return true
	default:
		return false
	}
}
