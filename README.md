# GitHub Webhook Bot for Telegram

A robust Telegram bot that integrates with GitHub to send notifications, manage repositories, and interact with issues/PRs directly from your chat.

## Features

*   **Real-time Notifications**: Receive instant updates for Pushes, Issues, Pull Requests, Reviews, Forks, Stars, and more.
*   **Repository Management**: Add or remove repositories directly from Telegram (`/addrepo`, `/removerepo`).
*   **Auto-Discovery**: Automatically find and link repositories you have access to.
*   **Interactive Settings**: Configure which events to receive for each repository using a user-friendly inline menu (`/settings`).
*   **Direct Interaction**:
    *   **Reply to Threads**: Reply to a notification message in Telegram to post a comment on the corresponding GitHub Issue or PR.
    *   **Commands**: Reply to a notification with `/close`, `/reopen`, or `/approve` to perform the action directly.
    *   **Quick Actions**: Approve or Close Pull Requests via inline buttons (note: this feature is currently simplified).
*   **Privacy & Security**:
    *   Private chat only authentication (`/connect`).
    *   Encrypted storage of OAuth tokens.
    *   Role-based access control (Admin-only management commands).
    *   Strict privacy policy (`/privacy`).
*   **Stateless Webhooks**: Efficient handling of webhooks without database lookups for routing.

## Supported Events

*   **Code**: Push events
*   **Issues**: Open, close, reopen, edit, etc.
*   **Pull requests**: Open, close, review, etc.
*   **Wikis**: Gollum events
*   **Settings**: Repository updates
*   **Webhooks and services**: Meta events
*   **Deploy keys**: Key management
*   **Collaboration invites**: Member changes
*   **Forks**: Fork events
*   **Stars**: Watch events

## Prerequisites

*   **Go**: v1.23 or higher
*   **Docker & Docker Compose**: For containerized deployment.
*   **MongoDB**: v5.0+ (Provided via Docker Compose).
*   **Telegram Bot Token**: Get one from [@BotFather](https://t.me/BotFather).
*   **GitHub OAuth App**: Create one in Developer Settings.
    *   **Homepage URL**: Your bot's URL (e.g., `https://your-domain.com`).
    *   **Authorization callback URL**: `https://your-domain.com/oauth/callback`.

## Configuration

Copy `sample.env` to `.env` and configure the following variables:

```dotenv
# --- Telegram ---
# Your Telegram Bot Token
TELEGRAM_TOKEN=123456:ABC-DEF...
# Public URL where the bot is reachable
TELEGRAM_WEBHOOK_URL=https://your-domain.com

# --- GitHub OAuth ---
# From your GitHub OAuth App
GITHUB_CLIENT_ID=Iv1...
# From your GitHub OAuth App
GITHUB_CLIENT_SECRET=0123...

# --- Webhooks ---
# A strong random string shared between GitHub and the bot to validate payloads. (openssl rand -hex 16)
GITHUB_WEBHOOK_SECRET=your_webhook_secret_here

# --- Database ---
MONGODB_URI=mongodb://mongo:27017
DATABASE_NAME=github_bot

# --- Security ---
# 32-byte (64 hex chars) string for encrypting tokens in the DB.
# Generate with: openssl rand -hex 32
ENCRYPTION_KEY=a1b2c3d4...

# --- Server ---
PORT=8080
```

## Installation & Deployment

### Using Docker Compose (Recommended)

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/AshokShau/GithubBot.git
    cd GithubBot
    ```

2.  **Setup Environment:**
    ```bash
    cp sample.env .env
    # Edit .env with your credentials
    ```

3.  **Run the application:**
    ```bash
    docker-compose up -d --build
    ```

4.  **Verify:**
    The bot should be running on `http://localhost:8080`. GitHub will send webhooks to `https://your-domain.com/webhook/...`.

### Manual Build

1.  Install dependencies: `go mod download`
2.  Build the binary: `go build -o bot cmd/bot/main.go`
3.  Run: `./bot`

## Usage

1.  **Start the bot**: Send `/start` to your bot.
2.  **Connect GitHub**: Send `/connect` (in a private chat) to authenticate with GitHub.
3.  **Add a Repository**:
    *   Group Chat: `/addrepo owner/repo`
    *   Or use `/addrepo` without arguments to browse your repositories interactively.
4.  **Configure Notifications**:
    *   Send `/settings` to view linked repositories.
    *   Select a repository to customize events (e.g., enable "Issues" but disable "Stars").
5.  **Interact**:
    *   When you receive an issue notification, simply **reply** to that message in Telegram to post a comment on GitHub.

## Commands

*   `/start` - Start the bot and see the welcome message.
*   `/help` - Show available commands and help text.
*   `/connect` - Connect your GitHub account (Private chat only).
*   `/addrepo [owner/repo]` - Link a repository to the current chat.
*   `/removerepo [owner/repo]` - Unlink a repository.
*   `/settings` - Manage notification settings for linked repositories.
*   `/repos` - List all repositories linked to the current chat.
*   `/privacy` - View the privacy policy.
*   `/logout` - Disconnect your GitHub account.
*   `/reload` - Refresh admin cache (Admin only).
*   `/close` - Close an issue or PR (reply to notification).
*   `/reopen` - Reopen an issue or PR (reply to notification).
*   `/approve` - Approve a PR (reply to notification).

## Architecture

*   **Bot Framework**: `gotgbot` for Telegram Bot API.
*   **GitHub Integration**: `go-github` for API calls and webhook handling.
*   **Database**: MongoDB for storing user tokens (encrypted) and chat-repo links.
*   **Security**: AES-GCM encryption for stored OAuth tokens.
*   **Stateless Webhooks**: The webhook URL path contains an encrypted token representing the Chat ID, allowing the bot to route events without database lookups during the webhook request.

## Contributing

Pull requests are welcome! Please ensure you follow the coding standards and update tests as appropriate.

## License

This project is licensed under the MIT License - see the `LICENSE` file for details.

---

## BT / Support

**Maintained by:** [AshokShau](https://github.com/AshokShau)  
**Telegram:** https://t.me/FallenProjects

### Bug / Issue Tracking (BT)
- Found a bug or unexpected behavior?
- Please open a **GitHub Issue** with proper logs/details.

### Support
- General support & updates are shared on [Telegram](https://t.me/FallenProjects).

---

⭐ If this project helps you, consider giving it a star.
