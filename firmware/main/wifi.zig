const wifi_core = @import("core/wifi_core.zig");

var state = wifi_core.State{};

export fn sebastian_wifi_begin(trial: bool) callconv(.c) void {
    state = .{ .trial = trial };
}

export fn sebastian_wifi_got_ip() callconv(.c) void {
    state.gotIp();
}

export fn sebastian_wifi_next_attempt_ms() callconv(.c) i32 {
    return switch (state.disconnected()) {
        .retry_in_ms => |ms| @intCast(ms),
        .give_up => -1,
    };
}
