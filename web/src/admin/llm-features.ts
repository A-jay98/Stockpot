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
    name: 'recipes_parse',
    what: 'Reads a recipe from a page without recipe data, a reel caption and transcript, or pasted text.',
    withoutAI:
      'Only pages with recipe data and clearly laid-out pasted text import; the rest wait.',
  },
  {
    name: 'recipes_translate',
    what: 'Translates imported recipes into English, keeping the original.',
    withoutAI: 'The import waits for quota, then translates.',
  },
  {
    name: 'recipes_photo',
    what: 'Reads photos of a cookbook page, card or screenshot.',
    withoutAI: 'Paste the recipe text instead.',
  },
  {
    name: 'recipes_transcribe',
    what: 'Transcribes the spoken recipe in an Instagram reel (needs a provider with an audio model).',
    withoutAI: 'Reels import from the caption alone.',
  },
  {
    name: 'recipes_substitute',
    what: '"Find a swap" for a missing ingredient, using only what is in stock.',
    withoutAI: 'Seeded swaps still show; swap it by hand while you cook.',
  },
  {
    name: 'recipes_ideas',
    what: '"More ideas": up to 3 new recipes from what is in stock and expiring.',
    withoutAI: 'Saved recipes are still ranked by what you have; import or add one.',
  },
  {
    name: 'report_review',
    what: 'Reviews problem reports; applies default-unit fixes it is sure about (background).',
    withoutAI: 'Reports wait on Admin → Reports for you to apply, reject or dismiss.',
  },
]
