-- Modify "devices" table
ALTER TABLE "devices" ADD COLUMN "running_config" jsonb NULL, ADD COLUMN "running_config_at" timestamptz NULL;
