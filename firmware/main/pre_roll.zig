//! Local pre-roll buffer captured before LiveKit connects.
//!
//! In IDLE the wake word task owns I2S, so this module is fed from
//! wakeword.zig, not mic_src.zig. Nothing leaves the device until a wake word
//! fires and the room is connected.

const std = @import("std");
const c = @import("csdk.zig");
const core = @import("core/pre_roll_core.zig");

const log = std.log.scoped(.pre_roll);

pub const SAMPLE_RATE = core.SAMPLE_RATE;
pub const SECONDS = core.SECONDS;
pub const SAMPLE_CAPACITY = core.SAMPLE_CAPACITY;

const TOPIC = "sebastian.preroll";

var ring: ?[*]i16 = null;
var write_idx: usize = 0;
var filled: usize = 0;
var total_written: u64 = 0;
var wake_mark: u64 = 0;

/// The ring is always full (the wake task feeds it from boot), so the SEND
/// window is anchored to the wake mark: this much audio before the wake word,
/// plus everything after it up to the handoff. Without the anchor we'd ship
/// 12s of stale room noise ahead of the request every session.
pub const WAKE_LEAD_SAMPLES: usize = SAMPLE_RATE * 2;

pub fn init() bool {
    if (ring != null) return true;

    const bytes = SAMPLE_CAPACITY * @sizeOf(i16);
    const mem = c.heap_caps_malloc(bytes, c.MALLOC_CAP_SPIRAM | c.MALLOC_CAP_8BIT) orelse {
        log.err("PSRAM allocation failed ({d} bytes)", .{bytes});
        return false;
    };
    ring = @ptrCast(@alignCast(mem));
    reset();
    log.info("allocated {d} ms local pre-roll ({d} bytes)", .{ SECONDS * 1000, bytes });
    return true;
}

pub fn reset() void {
    write_idx = 0;
    filled = 0;
    total_written = 0;
    wake_mark = 0;
}

pub fn feed(samples: []const i16) void {
    const buf = ring orelse return;
    for (samples) |sample| {
        buf[write_idx] = sample;
        write_idx = (write_idx + 1) % SAMPLE_CAPACITY;
        if (filled < SAMPLE_CAPACITY) filled += 1;
    }
    total_written += samples.len;
}

/// Called by the wake task at the detection instant.
pub fn markWake() void {
    wake_mark = total_written;
}

fn windowSamples() usize {
    return core.windowSamples(wake_mark, total_written, filled);
}

pub fn availableMs() u32 {
    return core.availableMs(windowSamples());
}

fn writeSamples(room: c.livekit_room_handle_t, stream: c.livekit_data_stream_handle_t, start: usize, count: usize) bool {
    if (count == 0) return true;
    const buf = ring orelse return false;
    const slice = buf[start .. start + count];
    const bytes: [*]const u8 = @ptrCast(slice.ptr);
    return c.livekit_room_data_stream_write(room, stream, bytes, slice.len * @sizeOf(i16)) == c.LIVEKIT_ERR_NONE;
}

pub fn send(room: c.livekit_room_handle_t, wake_id: u32) bool {
    const count = windowSamples();
    if (ring == null or count == 0) {
        log.warn("no pre-roll available for wake_id={d}", .{wake_id});
        return false;
    }

    const sample_count: u32 = @intCast(count);
    const total_len: u64 = core.HEADER_BYTES + @as(u64, sample_count) * @sizeOf(i16);
    var opts = c.livekit_data_stream_options_t{
        .topic = TOPIC,
        .is_text = false,
        .total_length = total_len,
        .has_total_length = true,
    };
    var stream: c.livekit_data_stream_handle_t = null;
    if (c.livekit_room_data_stream_open(room, &opts, &stream) != c.LIVEKIT_ERR_NONE or stream == null) {
        log.err("open data stream failed for wake_id={d}", .{wake_id});
        return false;
    }

    var ok = true;
    const header = core.makeHeader(wake_id, sample_count);
    ok = ok and c.livekit_room_data_stream_write(room, stream, header[0..].ptr, header.len) == c.LIVEKIT_ERR_NONE;

    // Newest `count` samples end at write_idx; start may wrap.
    const start = core.startIndex(write_idx, count);
    const first = @min(count, SAMPLE_CAPACITY - start);
    ok = ok and writeSamples(room, stream, start, first);
    ok = ok and writeSamples(room, stream, 0, count - first);

    // Close unconditionally — skipping it on a failed write leaks the handle.
    const closed = c.livekit_room_data_stream_close(room, stream) == c.LIVEKIT_ERR_NONE;
    ok = ok and closed;
    if (ok) {
        log.info("sent pre-roll wake_id={d} duration_ms={d} bytes={d}", .{
            wake_id,
            availableMs(),
            total_len,
        });
    } else {
        log.err("send pre-roll failed wake_id={d}", .{wake_id});
    }
    return ok;
}
