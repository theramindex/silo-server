-- +goose Up
CREATE TABLE premiere_preferences (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    window_minutes integer NOT NULL DEFAULT 60 CHECK (window_minutes BETWEEN 15 AND 240),
    PRIMARY KEY (user_id, profile_id)
);
CREATE TABLE premiere_channel_mappings (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    broadcaster text NOT NULL,
    series_id text NOT NULL DEFAULT '',
    channel_ids text[] NOT NULL,
    window_minutes integer NOT NULL DEFAULT 60 CHECK (window_minutes BETWEEN 15 AND 240),
    PRIMARY KEY (user_id, profile_id, broadcaster, series_id)
);
CREATE UNIQUE INDEX premiere_one_series_mapping ON premiere_channel_mappings (user_id, profile_id, series_id) WHERE series_id <> '';
CREATE UNIQUE INDEX premiere_one_network_mapping ON premiere_channel_mappings (user_id, profile_id, lower(broadcaster)) WHERE series_id = '';
-- Independent of inbox retention: an episode can notify a profile only once.
CREATE TABLE premiere_receipts (
    user_id integer NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id text NOT NULL,
    episode_key text NOT NULL,
    notified_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, profile_id, episode_key)
);

-- +goose Down
DROP TABLE premiere_receipts;
DROP TABLE premiere_channel_mappings;
DROP TABLE premiere_preferences;
