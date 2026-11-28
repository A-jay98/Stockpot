-- Blobs (m2-2): who may read an uploaded photo, and cached thumbnails.
-- Additive only, not synced. The bytes live in the content-addressed blob
-- store (SHA-256 ref); these tables only say who uploaded what.

-- +goose Up

-- A household may read a ref once one of its members uploaded those bytes.
-- Uploading bytes another household already has just adds a row here.
CREATE TABLE blob_owner (
    ref          text        NOT NULL CHECK (ref ~ '^[0-9a-f]{64}$'),
    household_id uuid        NOT NULL REFERENCES household (id) ON DELETE CASCADE,
    uploaded_by  uuid        REFERENCES app_user (id) ON DELETE SET NULL,
    size         bigint      NOT NULL CHECK (size > 0),
    content_type text        NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png', 'image/webp')),
    width        int         NOT NULL CHECK (width > 0),
    height       int         NOT NULL CHECK (height > 0),
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ref, household_id)
);
CREATE INDEX blob_owner_household ON blob_owner (household_id, created_at);

-- A resized copy of a blob, itself stored as a blob (JPEG). Derived data:
-- safe to delete, it is rebuilt on the next request.
CREATE TABLE blob_variant (
    ref         text        NOT NULL CHECK (ref ~ '^[0-9a-f]{64}$'),
    width       int         NOT NULL CHECK (width > 0),
    variant_ref text        NOT NULL CHECK (variant_ref ~ '^[0-9a-f]{64}$'),
    size        bigint      NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ref, width)
);

-- +goose Down
DROP TABLE blob_variant;
DROP TABLE blob_owner;
