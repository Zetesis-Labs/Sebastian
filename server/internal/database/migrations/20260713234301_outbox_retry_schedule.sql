-- Drop index "outbox_events_pending_idx" from table: "outbox_events"
DROP INDEX "public"."outbox_events_pending_idx";
-- Modify "outbox_events" table
ALTER TABLE "public"."outbox_events" ADD COLUMN "next_attempt_at" timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP;
-- Create index "outbox_events_pending_idx" to table: "outbox_events"
CREATE INDEX "outbox_events_pending_idx" ON "public"."outbox_events" ("next_attempt_at", "created_at") WHERE (published_at IS NULL);
