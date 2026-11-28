-- Recipe chat (architecture-m2 §10). Not synced: plain REST, online only.
-- An assistant message may carry a proposal: the fields of the recipe it
-- would change (applied to the current version when saved). Saving it through
-- recipe.version.create marks it saved. proposal_error says why a described
-- change could not be offered as a version.

-- +goose Up

CREATE TABLE recipe_chat_message (
    id               uuid        PRIMARY KEY,
    household_id     uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    recipe_id        uuid        NOT NULL REFERENCES recipe (id) ON DELETE CASCADE,
    version_id       uuid        NOT NULL REFERENCES recipe_version (id) ON DELETE CASCADE,
    role             text        NOT NULL CHECK (role IN ('user', 'assistant')),
    text             text        NOT NULL,
    proposal         jsonb,
    proposal_state   text        NOT NULL DEFAULT 'none' CHECK (proposal_state IN ('none', 'pending', 'saved', 'dismissed')),
    proposal_error   text,
    saved_version_id uuid        REFERENCES recipe_version (id) ON DELETE SET NULL,
    user_id          uuid        REFERENCES app_user (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT recipe_chat_proposal CHECK ((proposal IS NULL) = (proposal_state = 'none'))
);
CREATE INDEX recipe_chat_message_recipe ON recipe_chat_message (recipe_id, created_at, id);

-- +goose Down
DROP TABLE recipe_chat_message;
