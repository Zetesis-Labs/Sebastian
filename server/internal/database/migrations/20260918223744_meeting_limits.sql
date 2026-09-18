-- Modify "devices" table
ALTER TABLE "public"."devices" ADD COLUMN "meeting_silence_min" integer NOT NULL DEFAULT 10, ADD COLUMN "meeting_max_hours" integer NOT NULL DEFAULT 3;
-- Drop index "meetings_in_progress" from table: "meetings"
DROP INDEX "public"."meetings_in_progress";
-- Create index "meetings_in_progress" to table: "meetings"
CREATE INDEX "meetings_in_progress" ON "public"."meetings" ("state") WHERE ((state)::text = ANY ((ARRAY['requested'::character varying, 'recording'::character varying, 'closing'::character varying])::text[]));
