//! When to retry the WiFi after a disconnection. Only a new network on trial
//! gives up, so the boot path can restore the previous one; any other unit
//! retries forever, or one outage (or a router slower than the unit after a
//! power cut) leaves it offline until someone pulls the plug.

const std = @import("std");

pub const JOIN_ATTEMPTS: u32 = 20;
pub const FIRST_PAUSE_MS: u32 = 1_000;
pub const MAX_PAUSE_MS: u32 = 180_000;

pub const Next = union(enum) {
    retry_in_ms: u32,
    give_up,
};

pub const State = struct {
    trial: bool = false,
    online: bool = false,
    failures: u32 = 0,

    pub fn gotIp(self: *State) void {
        self.online = true;
        self.failures = 0;
    }

    pub fn disconnected(self: *State) Next {
        defer self.failures +|= 1;
        if (!self.trial or self.online) return .{ .retry_in_ms = pauseAfter(self.failures) };
        if (self.failures >= JOIN_ATTEMPTS) return .give_up;
        return .{ .retry_in_ms = 0 };
    }
};

fn pauseAfter(failures: u32) u32 {
    const doublings = std.math.log2_int(u32, MAX_PAUSE_MS / FIRST_PAUSE_MS) + 1;
    if (failures >= doublings) return MAX_PAUSE_MS;
    return @min(MAX_PAUSE_MS, FIRST_PAUSE_MS << @intCast(failures));
}
