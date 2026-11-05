-- Drop index "meetings_in_progress" from table: "meetings"
DROP INDEX "public"."meetings_in_progress";
-- Create index "meetings_in_progress" to table: "meetings"
CREATE INDEX "meetings_in_progress" ON "public"."meetings" ("state") WHERE ((state)::text = ANY ((ARRAY['requested'::character varying, 'recording'::character varying, 'closing'::character varying])::text[]));
-- Drop "outbox_events" table
DROP TABLE "public"."outbox_events";
