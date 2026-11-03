-- Drop index "agent_profiles_name_key" from table: "agent_profiles"
DROP INDEX "public"."agent_profiles_name_key";
-- Modify "agent_profiles" table
ALTER TABLE "public"."agent_profiles" ADD CONSTRAINT "agent_profiles_source_check" CHECK ((source)::text = ANY ((ARRAY['kubernetes'::character varying, 'panel'::character varying])::text[])), ADD COLUMN "source" character varying NOT NULL DEFAULT 'panel', ADD COLUMN "api_key_ciphertext" bytea NULL, ADD COLUMN "api_key_updated_at" timestamptz NULL, ADD COLUMN "deleted_at" timestamptz NULL;
-- Create index "agent_profiles_name_key" to table: "agent_profiles"
CREATE UNIQUE INDEX "agent_profiles_name_key" ON "public"."agent_profiles" ("name") WHERE (deleted_at IS NULL);
-- Drop index "meetings_in_progress" from table: "meetings"
DROP INDEX "public"."meetings_in_progress";
-- Create index "meetings_in_progress" to table: "meetings"
CREATE INDEX "meetings_in_progress" ON "public"."meetings" ("state") WHERE ((state)::text = ANY ((ARRAY['requested'::character varying, 'recording'::character varying, 'closing'::character varying])::text[]));
