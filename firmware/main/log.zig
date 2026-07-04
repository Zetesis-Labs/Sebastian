//! Bridge Zig's `std.log` onto ESP-IDF's logger.
//!
//! Using `std.log` (instead of calling `esp_log_write` with C `printf` format
//! strings) gives compile-time checked format strings and scoped logging, while
//! still routing through ESP-IDF so output shows up over the serial monitor.

const std = @import("std");
const c = @import("csdk.zig");

pub fn espLogFn(
    comptime level: std.log.Level,
    comptime scope: @TypeOf(.enum_literal),
    comptime format: []const u8,
    args: anytype,
) void {
    var buf: [256]u8 = undefined;
    const msg = std.fmt.bufPrintZ(&buf, format, args) catch return;
    const esp_level: c_uint = switch (level) {
        .err => c.ESP_LOG_ERROR,
        .warn => c.ESP_LOG_WARN,
        .info => c.ESP_LOG_INFO,
        .debug => c.ESP_LOG_DEBUG,
    };
    // @tagName is null-terminated; "%s" keeps the message free of printf parsing.
    // Pass the tag through for IDF level filtering and print "tag: msg\n".
    const tag = @tagName(scope);
    c.esp_log_write(esp_level, tag, "%s: %s\n", tag, msg.ptr);
}
