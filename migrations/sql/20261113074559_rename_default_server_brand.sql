-- +goose Up
-- Rename the legacy default only. Explicit custom server names remain intact.
UPDATE server_settings
SET value = 'Ramedia Server'
WHERE key = 'branding.server_name' AND value = 'Silo';

-- +goose Down
-- A saved display name is a user preference; retain it on an image rollback.
SELECT 1;
