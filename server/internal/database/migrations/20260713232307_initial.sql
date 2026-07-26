-- Create "agent_profiles" table
CREATE TABLE "public"."agent_profiles" (
  "id" uuid NOT NULL,
  "name" character varying NOT NULL,
  "agent_name" character varying NOT NULL,
  "config" jsonb NOT NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "agent_profiles_name_key" to table: "agent_profiles"
CREATE UNIQUE INDEX "agent_profiles_name_key" ON "public"."agent_profiles" ("name");
-- Create "devices" table
CREATE TABLE "public"."devices" (
  "id" character varying NOT NULL,
  "display_name" character varying NOT NULL,
  "livekit_identity" character varying NOT NULL,
  "credential_digest" bytea NULL,
  "enabled" boolean NOT NULL DEFAULT true,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  "agent_profile_id" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "devices_agent_profiles_devices" FOREIGN KEY ("agent_profile_id") REFERENCES "public"."agent_profiles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "devices_livekit_identity_key" to table: "devices"
CREATE UNIQUE INDEX "devices_livekit_identity_key" ON "public"."devices" ("livekit_identity");
-- Create "domain_events" table
CREATE TABLE "public"."domain_events" (
  "id" uuid NOT NULL,
  "aggregate_type" character varying NOT NULL,
  "aggregate_id" character varying NOT NULL,
  "event_type" character varying NOT NULL,
  "event_version" bigint NOT NULL,
  "payload" jsonb NOT NULL,
  "occurred_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);
-- Create index "domain_events_aggregate_idx" to table: "domain_events"
CREATE INDEX "domain_events_aggregate_idx" ON "public"."domain_events" ("aggregate_type", "aggregate_id", "occurred_at");
-- Create "outbox_events" table
CREATE TABLE "public"."outbox_events" (
  "id" uuid NOT NULL,
  "subject" character varying NOT NULL,
  "payload" jsonb NOT NULL,
  "created_at" timestamptz NOT NULL,
  "published_at" timestamptz NULL,
  "attempts" bigint NOT NULL DEFAULT 0,
  "last_error" character varying NULL,
  "event_id" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "outbox_events_domain_events_outbox_event" FOREIGN KEY ("event_id") REFERENCES "public"."domain_events" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "outbox_events_event_id_key" to table: "outbox_events"
CREATE UNIQUE INDEX "outbox_events_event_id_key" ON "public"."outbox_events" ("event_id");
-- Create index "outbox_events_pending_idx" to table: "outbox_events"
CREATE INDEX "outbox_events_pending_idx" ON "public"."outbox_events" ("created_at") WHERE (published_at IS NULL);
-- Create "sessions" table
CREATE TABLE "public"."sessions" (
  "id" uuid NOT NULL,
  "room_name" character varying NOT NULL,
  "expires_at" timestamptz NOT NULL,
  "created_at" timestamptz NOT NULL,
  "agent_profile_id" uuid NOT NULL,
  "device_id" character varying NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "sessions_agent_profiles_sessions" FOREIGN KEY ("agent_profile_id") REFERENCES "public"."agent_profiles" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "sessions_devices_sessions" FOREIGN KEY ("device_id") REFERENCES "public"."devices" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "sessions_device_created_idx" to table: "sessions"
CREATE INDEX "sessions_device_created_idx" ON "public"."sessions" ("device_id", "created_at");
-- Create index "sessions_room_name_key" to table: "sessions"
CREATE UNIQUE INDEX "sessions_room_name_key" ON "public"."sessions" ("room_name");

-- Seed the compatibility identity used by the currently deployed firmware.
INSERT INTO "public"."agent_profiles" (
  "id", "name", "agent_name", "config", "created_at", "updated_at"
) VALUES (
  '018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8',
  'default',
  'sebastian',
  '{}',
  now(),
  now()
);

INSERT INTO "public"."devices" (
  "id", "display_name", "livekit_identity", "credential_digest", "enabled",
  "created_at", "updated_at", "agent_profile_id"
) VALUES (
  'esp32-respeaker',
  'Sebastian ESP32-S3',
  'esp32-respeaker',
  NULL,
  true,
  now(),
  now(),
  '018f08d8-3f5d-7d5d-bd61-9b2ba12b58b8'
);
