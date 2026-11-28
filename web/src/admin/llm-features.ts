/**
 * Every LLM feature and what the app does without AI (quota gone, no provider, or AI turned off).
 * Admin → LLM lists them so it's clear what degrades. Add a row with each new feature.
 */
export const llmFeatures: { name: string; what: string; withoutAI: string }[] = [
  {
    name: 'catalog_enrich',
    what: 'Matches new products to ingredients, categories, icons and shelf life (background).',
    withoutAI: 'Products keep the rule-based match; you can pick the ingredient yourself.',
  },
  {
    name: 'recipes_chat',
    what: 'Answers questions about a recipe and proposes new versions.',
    withoutAI: 'Edit the recipe by hand.',
  },
  {
    name: 'report_review',
    what: 'Reviews problem reports; applies default-unit fixes it is sure about (background).',
    withoutAI: 'Reports wait on Admin → Reports for you to apply, reject or dismiss.',
  },
]
