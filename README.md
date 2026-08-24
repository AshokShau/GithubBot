# GitHub Webhook Bot for Telegram

A robust Telegram bot that integrates with GitHub to send real-time event notifications, manage repositories, and interact with issues, pull requests, actions, releases, and discussions directly from Telegram.

## Features

*   **Real-time Notifications**: Receive instant updates for Pushes, Issues, Pull Requests, Reviews, Forks, Stars, GitHub Actions, Discussions, and more.
*   **Repository Management**: Add, remove, star, watch, fork, archive, or list repositories directly from Telegram (`/addrepo`, `/removerepo`, `/star`, `/fork`, etc.).
*   **Auto-Discovery**: Interactively browse and link repositories accessible via your connected GitHub account.
*   **Interactive Settings**: Configure event notification preferences for each repository using inline menus (`/settings` or `/config`).
*   **Full GitHub Interaction**:
    *   **Reply Comments**: Reply directly to a notification message in Telegram to post a comment on GitHub.
    *   **Issues & PRs**: Create issues, assign members, label, milestone, lock/unlock, pin/unpin, approve, request changes, or merge PRs directly.
    *   **Actions & CI**: Monitor workflow runs, dispatch manual workflow runs, rerun failed jobs, cancel runs, and access job logs.
    *   **Releases & Changelogs**: View latest releases, draft new releases, and automatically generate changelogs/release notes.
    *   **Discussions**: Create new discussions and mark comments as answers directly from Telegram.
    *   **Search**: Search issues, pull requests, and repository code seamlessly.
*   **Privacy & Security**:
    *   Private chat authentication (`/connect`).
    *   AES-GCM encrypted storage of OAuth tokens in MongoDB.
    *   Role-based access control for administrative commands (`/reload`).
    *   Transparent privacy policy (`/privacy`).
*   **Stateless Webhooks**: Efficient webhook routing using encrypted payload tokens in webhook URLs without extra database lookups during incoming requests.

## Supported Events

*   **Code**: Push events & Commits
*   **Issues**: Open, close, reopen, label, comment, assign, lock, pin, etc.
*   **Pull Requests**: Open, close, review, approve, request changes, merge, draft status, check runs, etc.
*   **GitHub Actions**: Workflow runs, workflow jobs
*   **Releases & Discussions**: Release creation, discussion posts & answered comments
*   **Wikis**: Gollum events
*   **Settings**: Repository updates
*   **Deploy Keys & Collaborators**: Member changes and access key management
*   **Forks & Stars**: Fork and watch/star events

## Prerequisites

*   **Go**: v1.23 or higher
*   **Docker & Docker Compose**: For containerized deployment.
*   **MongoDB**: v5.0+ (Provided via Docker Compose or MongoDB Atlas).
*   **Telegram Bot Token**: Created via [@BotFather](https://t.me/BotFather).
*   **GitHub OAuth App**: Configured in GitHub Developer Settings.
    *   **Homepage URL**: Your domain/bot URL (e.g., `https://your-domain.com`).
    *   **Authorization callback URL**: `https://your-domain.com/oauth/callback`.

## Configuration

Copy `sample.env` to `.env` and fill in the required variables:

```dotenv
# --- Telegram ---
# Your Telegram Bot Token from @BotFather
TELEGRAM_TOKEN=123456:ABC-DEF...
# Public URL where the bot server is reachable
TELEGRAM_WEBHOOK_URL=https://your-domain.com

# --- GitHub OAuth ---
# From your GitHub OAuth App settings
GITHUB_CLIENT_ID=Iv1...
GITHUB_CLIENT_SECRET=0123...

# --- Webhooks ---
# Shared secret for validating GitHub webhook payloads (e.g. openssl rand -hex 16)
GITHUB_WEBHOOK_SECRET=your_webhook_secret_here

# --- Database ---
MONGODB_URI=mongodb://mongo:27017
DATABASE_NAME=github_bot

# --- Security ---
# 32-byte (64 hex characters) string for AES-GCM encryption of stored tokens.
# Generate with: openssl rand -hex 32
ENCRYPTION_KEY=a1b2c3d4...

# --- Server ---
PORT=8080

# --- Owner / Admin ---
# Telegram User ID of the owner
OWNER_ID=123456789
```

## Installation & Deployment

### Using Docker Compose (Recommended)

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/AshokShau/GithubBot.git
    cd GithubBot
    ```

2.  **Configure environment:**
    ```bash
    cp sample.env .env
    # Edit .env with your credentials
    ```

3.  **Start the services:**
    ```bash
    docker-compose up -d --build
    ```

4.  **Verify installation:**
    The bot web server runs on `http://localhost:8080`. GitHub webhooks are received at `https://your-domain.com/webhook/...`.

### Manual Build

1.  Install Go dependencies:
    ```bash
    go mod download
    ```
2.  Build the binary:
    ```bash
    go build -o bot main.go
    ```
3.  Run the application:
    ```bash
    ./bot
    ```

## Usage

1.  **Start the Bot**: Send `/start` in Telegram.
2.  **Connect GitHub**: Send `/connect` (in a private chat) to authenticate with your GitHub account via OAuth.
3.  **Add Repositories**:
    *   In a group/channel: `/addrepo owner/repo` (or alias `/add owner/repo`).
    *   Or send `/addrepo` without arguments to select interactively from your accessible GitHub repositories.
4.  **Manage Notifications**:
    *   Send `/settings` (or alias `/config`) to view linked repositories and toggle event notifications using inline menus.
5.  **Interact via Telegram**:
    *   **Reply to Notifications**: Simply reply to any issue or PR notification message to post a comment on GitHub.
    *   **Execute Quick Actions**: Reply with commands like `/close`, `/reopen`, `/approve`, or `/merge` to update GitHub state immediately.

## Commands

### Authentication
*   `/connect` - Connect your GitHub account via OAuth (Private chat only).
*   `/disconnect`, `/logout` - Disconnect your GitHub account.
*   `/me` - Display your connected GitHub account profile details.

### Repository Management
*   `/addrepo [owner/repo]`, `/add [owner/repo]` - Link a repository to the chat.
*   `/removerepo [owner/repo]`, `/rm [owner/repo]` - Unlink a repository from the chat.
*   `/repos` - List all repositories linked to the current chat.
*   `/repo [owner/repo]` - Show repository summary, statistics, and URL.
*   `/star` - Star the repository.
*   `/unstar` - Remove your star from the repository.
*   `/watch` - Watch the repository.
*   `/unwatch` - Stop watching the repository.
*   `/fork` - Fork the repository to your GitHub account.
*   `/archive` - Archive the repository.
*   `/unarchive` - Unarchive the repository.
*   `/contributors` - List top contributors and commit counts.
*   `/languages` - Show language composition breakdown percentages.
*   `/branches` - List repository branches.
*   `/branch <branch-name>` - Show branch details and latest commit.
*   `/default <branch-name>` - Change repository default branch.

### Issues
*   `/issue <Title>` - Create a new issue (supports multi-line body).
*   `/comment <text>` - Post a comment on an issue or PR (reply to notification).
*   `/close` - Close an issue or PR (reply to notification).
*   `/reopen` - Reopen a closed issue or PR (reply to notification).
*   `/assign @username` - Assign a user to the issue or PR.
*   `/assignme` - Assign yourself to the issue or PR.
*   `/unassign @username` - Unassign a user.
*   `/label +<label1> -<label2>` - Add or remove labels (e.g. `/label +bug -help-wanted`).
*   `/labels` - List labels for the issue or repository.
*   `/milestone <name>` - Assign a milestone to an issue or PR.
*   `/lock` - Lock conversation on an issue or PR.
*   `/unlock` - Unlock conversation on an issue or PR.
*   `/pin` - Pin an issue.
*   `/unpin` - Unpin an issue.

### Pull Requests
*   `/approve` - Approve a Pull Request (reply to notification).
*   `/requestchanges [text]` - Request changes on a Pull Request.
*   `/merge [squash|rebase|merge]` - Merge a Pull Request with specified strategy.
*   `/draft` - Convert a Pull Request to Draft status.
*   `/ready` - Mark a Draft Pull Request as Ready for Review.
*   `/checks` - View CI/CD check runs status for the head commit.
*   `/files` - List changed files and addition/deletion line counts.
*   `/diff` - Display change summary (files changed, additions, deletions).
*   `/reviews` - List review history and approval status.
*   `/mergeable` - Check if a Pull Request has merge conflicts.
*   `/request @username` - Request a review from a GitHub user.

### Commits
*   `/commit <SHA>` - View details for a specific commit SHA.
*   `/commits` - View recent commit history.
*   `/compare <branch1> <branch2>` - Compare two branches or commits.

### GitHub Actions
*   `/actions` - List recent workflow runs and statuses.
*   `/run <workflow.yml> [branch]` - Manually trigger a workflow run.
*   `/rerun` - Rerun a failed workflow run (reply to notification).
*   `/cancel` - Cancel an active workflow run (reply to notification).
*   `/logs` - Get a link to workflow run logs.

### Releases
*   `/release` - View details for the latest release.
*   `/release create <tag>` - Create a new release with automatically generated notes.
*   `/changelog [tag]` - Generate release notes / changelog for a tag or next release.

### Discussions
*   `/discussion <Title>` - Create a new discussion (supports multi-line body).
*   `/answered` - Mark a discussion comment as the answer (reply to notification).

### Search
*   `/find <keyword>` - Search issues within the repository.
*   `/pr <keyword>` - Search pull requests within the repository.
*   `/search <keyword>` - Search repository code.

### Notifications & Statistics
*   `/mute` - Mute notifications for the current forum topic/thread.
*   `/done` - Mark corresponding GitHub notification as done (reply to notification).
*   `/read` - Mark corresponding GitHub notification as read (reply to notification).
*   `/stats` - Show repository summary statistics (stars, forks, open issues/PRs, contributors).
*   `/activity` - Show recent activity stream for the repository.

### Settings & System
*   `/settings`, `/config` - Manage event notification preferences for linked repositories.
*   `/reload` - Reload administrator permission cache (Admins only).
*   `/privacy` - View data privacy policy.
*   `/help` - View complete command list and usage help.


## Contributing

Pull requests are welcome! Please ensure you follow standard Go coding practices, update documentation, and verify tests pass before submitting.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

---

## Support & Maintainer

**Maintained by:** [AshokShau](https://github.com/AshokShau)  
**Telegram:** https://t.me/FallenProjects

### Bug / Issue Tracking
If you encounter a bug or unexpected behavior, please open an issue on [GitHub](https://github.com/AshokShau/GithubBot/issues) with detailed reproduction steps and logs.

### Support
For general queries, announcements, and support, join our channel on [Telegram](https://t.me/FallenProjects).

---

⭐ If this project helps you, consider giving it a star.
