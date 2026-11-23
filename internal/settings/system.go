package settings

// System-wide settings that belong to no single feature package.

// LogLevel is the minimum level written to stdout and the app_log table.
var LogLevel = Define(Def[string]{
	Key:         "log.level",
	Default:     "info",
	Options:     []string{"debug", "info", "warn", "error"},
	Description: "Minimum level of application log lines (stdout and System logs).",
})

// Retention periods, in days, enforced by the daily retention job.
var (
	RetentionAppLogDays = Define(Def[int]{
		Key:         "retention.app_log_days",
		Default:     14,
		Validate:    Between(1, 3650),
		Description: "Days to keep application log lines.",
	})
	RetentionAccessLogDays = Define(Def[int]{
		Key:         "retention.access_log_days",
		Default:     90,
		Validate:    Between(1, 3650),
		Description: "Days to keep the per-request access log.",
	})
	RetentionAuditDays = Define(Def[int]{
		Key:         "retention.audit_days",
		Default:     730,
		Validate:    Between(30, 3650),
		Description: "Days to keep audit events (logins, approvals, setting changes, data edits).",
	})
)
