package catalog

import (
	"github.com/a-jay98/stockpot/internal/settings"
)

// Enrichment settings, edited on Admin → Catalog (and Settings).
var (
	FlagEnrich = settings.Define(settings.Def[bool]{
		Key:         "flag.catalog_enrich",
		Default:     true,
		Description: "Let the background job ask an LLM to match new products to ingredients (only with spare quota).",
	})
	SettingEnrichIntervalMinutes = settings.Define(settings.Def[int]{
		Key:         "enrich.interval_minutes",
		Default:     20,
		Validate:    settings.Between(1, 1440),
		Description: "Minutes between background enrichment runs.",
	})
	SettingEnrichBatchSize = settings.Define(settings.Def[int]{
		Key:         "enrich.batch_size",
		Default:     15,
		Validate:    settings.Between(1, 50),
		Description: "Products sent to the LLM in one call.",
	})
	SettingEnrichMaxCalls = settings.Define(settings.Def[int]{
		Key:         "enrich.max_calls_per_run",
		Default:     4,
		Validate:    settings.Between(1, 20),
		Description: "Most LLM calls one enrichment run may make.",
	})
	SettingEnrichRunRetentionDays = settings.Define(settings.Def[int]{
		Key:         "retention.enrich_run_days",
		Default:     60,
		Validate:    settings.Between(7, 3650),
		Description: "Days to keep enrichment run records and cached answers (answers: 180 days at least).",
	})
)

// Enrichment tuning that is not a setting.
const (
	// maxEnrichAttempts is how many invalid answers a product gets before it
	// is marked failed (the rule result stands).
	maxEnrichAttempts = 3
	// cacheDays is how long an answer for a name is reused.
	cacheDays = 180
	// candidatesPerItem is how many trigram ingredient candidates each item
	// carries in the prompt.
	candidatesPerItem = 8
	// maxBatchesPerRun bounds the loop when every batch is served from the cache.
	maxBatchesPerRun = 40
)
