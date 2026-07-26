CREATE TABLE agent_profiles (
  id uuid PRIMARY KEY,
  name varchar NOT NULL,
  agent_name varchar NOT NULL,
  config jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX agent_profiles_name_key ON agent_profiles (name);

CREATE TABLE devices (
  id varchar PRIMARY KEY,
  display_name varchar NOT NULL,
  livekit_identity varchar NOT NULL,
  credential_digest bytea,
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  agent_profile_id uuid NOT NULL,
  CONSTRAINT devices_agent_profiles_devices
    FOREIGN KEY (agent_profile_id) REFERENCES agent_profiles(id)
);

CREATE UNIQUE INDEX devices_livekit_identity_key ON devices (livekit_identity);

CREATE TABLE domain_events (
  id uuid PRIMARY KEY,
  aggregate_type varchar NOT NULL,
  aggregate_id varchar NOT NULL,
  event_type varchar NOT NULL,
  event_version bigint NOT NULL,
  payload jsonb NOT NULL,
  occurred_at timestamptz NOT NULL
);

CREATE INDEX domain_events_aggregate_idx
  ON domain_events (aggregate_type, aggregate_id, occurred_at);

CREATE TABLE outbox_events (
  id uuid PRIMARY KEY,
  event_id uuid NOT NULL,
  subject varchar NOT NULL,
  payload jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  published_at timestamptz,
  attempts bigint NOT NULL DEFAULT 0,
  last_error varchar,
  next_attempt_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
  CONSTRAINT outbox_events_domain_events_outbox_event
    FOREIGN KEY (event_id) REFERENCES domain_events(id)
);

CREATE UNIQUE INDEX outbox_events_event_id_key ON outbox_events (event_id);

CREATE INDEX outbox_events_pending_idx
  ON outbox_events (next_attempt_at, created_at)
  WHERE published_at IS NULL;

CREATE TABLE sessions (
  id uuid PRIMARY KEY,
  room_name varchar NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  agent_profile_id uuid NOT NULL,
  device_id varchar NOT NULL,
  CONSTRAINT sessions_agent_profiles_sessions
    FOREIGN KEY (agent_profile_id) REFERENCES agent_profiles(id),
  CONSTRAINT sessions_devices_sessions
    FOREIGN KEY (device_id) REFERENCES devices(id)
);

CREATE UNIQUE INDEX sessions_room_name_key ON sessions (room_name);

CREATE INDEX sessions_device_created_idx ON sessions (device_id, created_at);

CREATE TABLE recordings (
  id uuid PRIMARY KEY,
  session_id uuid NOT NULL,
  kind varchar NOT NULL,
  file_name varchar NOT NULL,
  object_url varchar NOT NULL,
  content_type varchar NOT NULL DEFAULT 'audio/wav',
  byte_size bigint NOT NULL,
  duration_ms bigint NOT NULL,
  captured_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  transcript varchar,
  CONSTRAINT recordings_sessions_recordings
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE UNIQUE INDEX recordings_object_url_key ON recordings (object_url);

CREATE INDEX recording_captured_at_id ON recordings (captured_at, id);
CREATE INDEX recording_session_id_kind ON recordings (session_id, kind);
