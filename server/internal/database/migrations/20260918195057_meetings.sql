-- Create "meetings" table
CREATE TABLE "meetings" (
  "id" uuid NOT NULL,
  "device_id" character varying NOT NULL,
  "session_id" uuid NULL,
  "state" character varying NOT NULL,
  "requested_by" character varying NOT NULL,
  "requested_at" timestamptz NOT NULL,
  "started_at" timestamptz NULL,
  "ended_at" timestamptz NULL,
  "end_reason" character varying NULL,
  "audio_path" character varying NULL,
  "audio_bytes" bigint NOT NULL DEFAULT 0,
  "last_audio_at" timestamptz NULL,
  "duration_ms" bigint NOT NULL DEFAULT 0,
  "transcript" jsonb NULL,
  "transcript_error" character varying NULL,
  "summary" jsonb NULL,
  "keep" boolean NOT NULL DEFAULT false,
  "deleted_at" timestamptz NULL,
  "created_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "meetings_devices_meetings" FOREIGN KEY ("device_id") REFERENCES "devices" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION,
  CONSTRAINT "meetings_sessions_meetings" FOREIGN KEY ("session_id") REFERENCES "sessions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "meetings_device_started" to table: "meetings"
CREATE INDEX "meetings_device_started" ON "meetings" ("device_id", "started_at" DESC);
-- Create index "meetings_in_progress" to table: "meetings"
CREATE INDEX "meetings_in_progress" ON "meetings" ("state") WHERE ((state)::text = ANY ((ARRAY['requested'::character varying, 'recording'::character varying, 'closing'::character varying])::text[]));
