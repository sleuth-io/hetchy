# Slack App Setup

If you want to use the Slack integration, follow these steps to create and configure a Slack app. This process is error-prone, so follow each step carefully.

## 1. Create a New Slack App

1. Go to [https://api.slack.com/apps](https://api.slack.com/apps)
2. Click **"Create New App"**
3. Select **"From scratch"**
4. Enter an app name (e.g., "Hetchy Bot" or "Code Assistant")
5. Select the workspace where you want to install the app
6. Click **"Create App"**

## 2. Enable Socket Mode

**Socket Mode is required** for the bot to receive events without exposing a public webhook URL.

1. In your app settings, navigate to **"Socket Mode"** (under Settings in the left sidebar)
2. Toggle **"Enable Socket Mode"** to **ON**
3. You'll be prompted to create an app-level token:
   - Enter a token name (e.g., "Socket Token" or "WebSocket Connection")
   - Add the scope `connections:write` (should be pre-selected)
   - Click **"Generate"**
4. **Copy the token** (it starts with `xapp-`) and save it securely — this is your `SLACK_SOCKET_TOKEN`
5. Click **"Done"**

## 3. Configure OAuth & Permissions

1. Navigate to **"OAuth & Permissions"** (under Features in the left sidebar)
2. Scroll down to the **"Scopes"** section
3. Under **"Bot Token Scopes"**, add the following scopes by clicking **"Add an OAuth Scope"**:

   **Required scopes:**
   - `app_mentions:read` — Listen for @mentions of the bot in channels
   - `chat:write` — Send messages as the bot
   - `channels:history` — View message history in public channels (needed to read thread context)
   - `groups:history` — View message history in private channels (needed to read thread context in private channels)
   - `im:history` — View message history in direct messages
   - `im:read` — View basic information about direct messages
   - `im:write` — Start direct messages with users

   The bot handles two types of events:
   - **App mentions** (`@bot do something`) in channels and threads
   - **Direct messages** to the bot

4. Scroll to the top of the **"OAuth & Permissions"** page
5. Click **"Install to Workspace"** (or "Reinstall to Workspace" if you've installed before)
6. Review the permissions and click **"Allow"**
7. **Copy the "Bot User OAuth Token"** (it starts with `xoxb-`) — this is your `SLACK_BOT_OAUTH_TOKEN`

## 4. Subscribe to Bot Events

1. Navigate to **"Event Subscriptions"** (under Features in the left sidebar)
2. Toggle **"Enable Events"** to **ON**
3. Scroll down to **"Subscribe to bot events"**
4. Click **"Add Bot User Event"** and add the following events:

   **Required events:**
   - `app_mention` — Fires when someone @mentions your bot in a channel or thread
   - `message.im` — Fires when someone sends a direct message to your bot

5. Click **"Save Changes"** at the bottom of the page

## 5. Configure App Home (Required for DM support)

**Without this step, users can still DM the bot by searching for it manually, but they won't see a Messages tab in the App Home view, making it harder to discover.**

1. Navigate to **"App Home"** (under Features in the left sidebar)
2. Scroll to the **"Show Tabs"** section
3. Check **"Allow users to send Slash commands and messages from the messages tab"**
4. This enables users to DM your bot directly from the app's home tab

## 6. Save Your Tokens to Doppler

You should now have two tokens:

1. **`SLACK_BOT_OAUTH_TOKEN`** (starts with `xoxb-`) — from OAuth & Permissions
2. **`SLACK_SOCKET_TOKEN`** (starts with `xapp-`) — from Socket Mode

Add these to Doppler:

```bash
# If you haven't set up Doppler yet, see the Quick Start guide in README.md
doppler secrets set SLACK_BOT_OAUTH_TOKEN="xoxb-your-token-here"
doppler secrets set SLACK_SOCKET_TOKEN="xapp-your-token-here"
```

## 7. Verify Installation

1. Start the bot (see [Quick Start](../README.md#quick-start))
2. Look for the log message: `slack socket connected — listening for events`
3. In your Slack workspace:
   - Invite the bot to a channel: `/invite @HetchyBot` (or whatever name you chose in Step 1)
   - Mention the bot: `@HetchyBot help`
   - Or send a direct message to the bot
4. The bot should respond in a thread with "Working on it…"

## Common Setup Issues

**Issue: "slack socket invalid auth"**
- Double-check both tokens are correctly copied to Doppler
- Ensure there are no extra spaces or newlines in the token values
- Verify you copied the full token (they're quite long)
- Confirm Socket Mode is enabled

**Issue: Bot doesn't respond to messages**
- Verify you've subscribed to `app_mention` and `message.im` events
- Check that you clicked "Save Changes" after adding events
- Ensure the bot is invited to the channel (for channel messages)
- Look for the `app_mentions:read` scope in OAuth & Permissions

**Issue: "Event type not supported"**
- Re-verify the Event Subscriptions section
- Make sure you added events under "Subscribe to bot events", not "Subscribe to workspace events"
- After changing event subscriptions, you may need to reinstall the app

**Issue: Bot responds to its own messages**
- This shouldn't happen — the bot filters out messages with a `bot_id`
- If it occurs, verify you're using Socket Mode (not webhooks) and that you have the correct event subscriptions
