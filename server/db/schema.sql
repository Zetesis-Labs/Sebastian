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
  -- Nullable: devices auto-registered by the desired-profile poll have no
  -- agent profile until an operator assigns one — without it FindDevice's
  -- JOIN excludes them, so they simply cannot open LiveKit sessions yet.
  agent_profile_id uuid,
  -- Device-side personality reconciliation (firmware NVS profiles, a concept
  -- DISTINCT from agent_profiles): the admin sets desired, the device reports
  -- what it runs on every poll and reboots into desired when they differ.
  desired_device_profile varchar,
  reported_device_profile varchar,
  profile_reported_at timestamptz,
  -- Fleet adoption (docs/implementation/11-fleet-adoption-control-room.md):
  -- credential_digest is the per-device secret issued at adoption; a
  -- regenerated secret waits in pending_credential_digest until the device
  -- first authenticates with it. forgotten_at hides the unit from the
  -- inventory while keeping its sessions.
  adopted_at timestamptz,
  pending_credential_digest bytea,
  desired_config jsonb,
  desired_config_version varchar,
  -- What the unit reports it runs (RF-42), without secrets; sent at boot.
  running_config jsonb,
  running_config_at timestamptz,
  reported_config_version varchar,
  reported_firmware varchar,
  -- The last event the unit reported in its poll (RF-36/42): adopt-denied:<ip>,
  -- cfg-rejected:<why> or an error of its previous boot; last_event_at is the
  -- first poll that carried that value.
  last_event varchar,
  last_event_at timestamptz,
  -- Meeting recordings (docs/implementation/13 RM-23/24): the silence that
  -- ends a recording and its maximum length, set from the unit's ficha.
  meeting_silence_min integer NOT NULL DEFAULT 10,
  meeting_max_hours integer NOT NULL DEFAULT 3,
  forgotten_at timestamptz,
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

-- Meeting recordings from the speaker (docs/implementation/13/14): a meeting
-- goes through requested → recording → closing → transcribing → ready |
-- no_transcript, or cut when the unit vanished. The audio lives on the
-- server's volume (audio_path, relative to SEBASTIAN_MEETINGS_DIR); deleting
-- keeps the row without content (RM-46).
CREATE TABLE meetings (
  id uuid PRIMARY KEY,
  device_id varchar NOT NULL,
  session_id uuid,
  state varchar NOT NULL,
  requested_by varchar NOT NULL,
  requested_at timestamptz NOT NULL,
  started_at timestamptz,
  ended_at timestamptz,
  end_reason varchar,
  audio_path varchar,
  audio_bytes bigint NOT NULL DEFAULT 0,
  last_audio_at timestamptz,
  duration_ms bigint NOT NULL DEFAULT 0,
  transcript jsonb,
  transcript_error varchar,
  summary jsonb,
  keep boolean NOT NULL DEFAULT false,
  deleted_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT meetings_devices_meetings
    FOREIGN KEY (device_id) REFERENCES devices(id),
  CONSTRAINT meetings_sessions_meetings
    FOREIGN KEY (session_id) REFERENCES sessions(id)
);

CREATE INDEX meetings_device_started ON meetings (device_id, started_at DESC);
CREATE INDEX meetings_in_progress ON meetings (state) WHERE state IN ('requested', 'recording', 'closing');

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
