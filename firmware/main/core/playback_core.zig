//! Pure playback choreography: which playback room the idle loop should be
//! in. The control room says which playback is on (the LAN order and every
//! poll); the shell opens and closes the room and reports back. No I/O here,
//! so the sequences (a stop that crossed a start, a server that forgot the
//! playback, a room that dropped) are tested on the host.
//!
//! The unit leaves the room to attend the wake word, an announcement or a
//! meeting and comes back while the playback is still wanted: leaving only
//! calls closed(), the wish stays.

const std = @import("std");

/// A playback id is a UUID in its 36-character text form.
pub const Id = [36]u8;

/// Between two attempts to open a room that failed or dropped.
pub const RETRY_MS: u32 = 10_000;
/// Polls in a row that do not mention our playback before we drop it: the
/// control room forgot it (a restart). One alone can be a poll that crossed
/// the LAN start.
pub const SILENT_POLLS_TO_DROP: u32 = 2;

pub fn parseId(text: []const u8) ?Id {
    if (text.len != 36) return null;
    var id: Id = undefined;
    @memcpy(&id, text);
    return id;
}

pub const Order = union(enum) {
    start: Id,
    stop: Id,
};

/// The poll's X-Playback header: "start:<id>" or "stop:<id>"; null for
/// anything else.
pub fn parseOrder(header: []const u8) ?Order {
    const colon = std.mem.indexOfScalar(u8, header, ':') orelse return null;
    const id = parseId(header[colon + 1 ..]) orelse return null;
    const verb = header[0..colon];
    if (std.mem.eql(u8, verb, "start")) return .{ .start = id };
    if (std.mem.eql(u8, verb, "stop")) return .{ .stop = id };
    return null;
}

pub const Action = union(enum) {
    none,
    open: Id,
    close,
};

/// Why opening the room failed: the control room says the playback is off
/// (stop wanting it), or anything else (try again later).
pub const OpenFailure = enum { off, transient };

pub const State = struct {
    want: ?Id = null,
    open: ?Id = null,
    silent_polls: u32 = 0,
    retry_at_ms: u64 = 0,

    pub fn order(self: *State, o: Order) void {
        self.silent_polls = 0;
        switch (o) {
            .start => |id| {
                const fresh = if (self.want) |w| !std.mem.eql(u8, &w, &id) else true;
                self.want = id;
                if (fresh) self.retry_at_ms = 0;
            },
            .stop => |id| {
                if (self.want) |w| {
                    if (std.mem.eql(u8, &w, &id)) self.want = null;
                }
            },
        }
    }

    /// Polls since the last order that did not mention any playback.
    pub fn silentPolls(self: *State, n: u32) void {
        if (n == 0 or self.want == null) return;
        self.silent_polls += n;
        if (self.silent_polls >= SILENT_POLLS_TO_DROP) {
            self.want = null;
            self.silent_polls = 0;
        }
    }

    pub fn next(self: State, now_ms: u64) Action {
        if (self.open) |o| {
            if (self.want) |w| {
                if (std.mem.eql(u8, &w, &o)) return .none;
            }
            return .close;
        }
        if (self.want) |w| {
            if (now_ms >= self.retry_at_ms) return .{ .open = w };
        }
        return .none;
    }

    pub fn opened(self: *State, id: Id) void {
        self.open = id;
    }

    /// The shell closed the room: because next() said so, or to leave for a
    /// session. The wish is untouched.
    pub fn closed(self: *State) void {
        self.open = null;
    }

    pub fn openFailed(self: *State, why: OpenFailure, now_ms: u64) void {
        switch (why) {
            .off => self.want = null,
            .transient => self.retry_at_ms = now_ms + RETRY_MS,
        }
    }

    /// The room dropped on its own (network, LiveKit): reopen after a pause.
    pub fn ended(self: *State, now_ms: u64) void {
        self.open = null;
        self.retry_at_ms = now_ms + RETRY_MS;
    }
};
