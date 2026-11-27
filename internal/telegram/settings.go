package telegram

import "github.com/a-jay98/stockpot/internal/settings"

// SettingBotToken is the Stockpot bot's API token, shared by every feature
// that talks to Telegram (backups, alerts).
var SettingBotToken = settings.Define(settings.Def[string]{
	Key:         "telegram.bot_token",
	Secret:      true,
	Description: "Telegram bot API token from @BotFather (used for backups and alerts).",
})
