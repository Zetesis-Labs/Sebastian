-- Create "recordings" table
CREATE TABLE "public"."recordings" (
  "id" uuid NOT NULL,
  "kind" character varying NOT NULL,
  "file_name" character varying NOT NULL,
  "object_url" character varying NOT NULL,
  "content_type" character varying NOT NULL DEFAULT 'audio/wav',
  "byte_size" bigint NOT NULL,
  "duration_ms" bigint NOT NULL,
  "captured_at" timestamptz NOT NULL,
  "created_at" timestamptz NOT NULL,
  "transcript" character varying NULL,
  "session_id" uuid NOT NULL,
  PRIMARY KEY ("id"),
  CONSTRAINT "recordings_sessions_recordings" FOREIGN KEY ("session_id") REFERENCES "public"."sessions" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION
);
-- Create index "recording_captured_at_id" to table: "recordings"
CREATE INDEX "recording_captured_at_id" ON "public"."recordings" ("captured_at", "id");
-- Create index "recording_session_id_kind" to table: "recordings"
CREATE INDEX "recording_session_id_kind" ON "public"."recordings" ("session_id", "kind");
-- Create index "recordings_object_url_key" to table: "recordings"
CREATE UNIQUE INDEX "recordings_object_url_key" ON "public"."recordings" ("object_url");
