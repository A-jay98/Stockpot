package reports

import "github.com/a-jay98/stockpot/internal/settings"

// Review settings, edited on Admin → Reports (and Settings).
var (
	FlagReview = settings.Define(settings.Def[bool]{
		Key:         "flag.report_review",
		Default:     true,
		Description: "Let the background job ask an LLM to review problem reports (only with spare quota).",
	})
	FlagAutoApply = settings.Define(settings.Def[bool]{
		Key:         "flag.report_auto_apply",
		Default:     true,
		Description: "Apply default-unit corrections the LLM approves without waiting for the admin.",
	})
	SettingReviewIntervalMinutes = settings.Define(settings.Def[int]{
		Key:         "reports.review_interval_minutes",
		Default:     30,
		Validate:    settings.Between(1, 1440),
		Description: "Minutes between background report reviews.",
	})
	SettingReviewBatchSize = settings.Define(settings.Def[int]{
		Key:         "reports.batch_size",
		Default:     10,
		Validate:    settings.Between(1, 30),
		Description: "Reports sent to the LLM in one call.",
	})
	SettingAutoApplyConfidence = settings.Define(settings.Def[int]{
		Key:         "reports.auto_apply_confidence_pct",
		Default:     80,
		Validate:    settings.Between(50, 100),
		Description: "Least LLM confidence (percent) to apply or reject a report on its own; below it the report waits for the admin.",
	})
	SettingRetentionDays = settings.Define(settings.Def[int]{
		Key:         "retention.problem_report_days",
		Default:     365,
		Validate:    settings.Between(30, 3650),
		Description: "Days to keep closed problem reports.",
	})
)

// Tuning that is not a setting.
const (
	// maxAttempts is how many unusable LLM answers a report gets before it
	// goes to the admin.
	maxAttempts = 3
	// maxCallsPerRun bounds one run.
	maxCallsPerRun = 3
)
