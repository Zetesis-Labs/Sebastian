-- Modify "devices" table
ALTER TABLE "public"."devices" ADD COLUMN "meeting_language" character varying NOT NULL DEFAULT 'es';
-- Drop index "meetings_in_progress" from table: "meetings"
DROP INDEX "public"."meetings_in_progress";
-- Create index "meetings_in_progress" to table: "meetings"
CREATE INDEX "meetings_in_progress" ON "public"."meetings" ("state") WHERE ((state)::text = ANY ((ARRAY['requested'::character varying, 'recording'::character varying, 'closing'::character varying])::text[]));
