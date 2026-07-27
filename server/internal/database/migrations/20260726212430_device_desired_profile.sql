-- Modify "devices" table
ALTER TABLE "public"."devices" ALTER COLUMN "agent_profile_id" DROP NOT NULL, ADD COLUMN "desired_device_profile" character varying NULL, ADD COLUMN "reported_device_profile" character varying NULL, ADD COLUMN "profile_reported_at" timestamptz NULL;
