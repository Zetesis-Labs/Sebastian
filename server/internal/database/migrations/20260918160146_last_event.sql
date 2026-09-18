-- Modify "devices" table
ALTER TABLE "devices" ADD COLUMN "last_event" character varying NULL, ADD COLUMN "last_event_at" timestamptz NULL;
