//! Pure parser for the token-server response.

const std = @import("std");

pub const Error = error{MalformedResponse};

pub const Parsed = struct {
    url: []const u8,
    token: []const u8,
};

/// Parse `<serverUrl>\n<token>`, trim CR/LF/spaces around both fields, copy
/// them to caller-owned nul-terminated buffers, and return non-nul slices.
pub fn parseResponse(body: []const u8, url_buf: []u8, token_buf: []u8) Error!Parsed {
    const nl = std.mem.indexOfScalar(u8, body, '\n') orelse return error.MalformedResponse;

    const url = std.mem.trim(u8, body[0..nl], " \r\n");
    const token = std.mem.trim(u8, body[nl + 1 ..], " \r\n");
    const invalid =
        url.len == 0 or
        token.len == 0 or
        url.len >= url_buf.len or
        token.len >= token_buf.len or
        !hasWebSocketScheme(url) or
        hasForbiddenByte(url) or
        hasForbiddenByte(token);
    if (invalid) {
        return error.MalformedResponse;
    }

    @memcpy(url_buf[0..url.len], url);
    url_buf[url.len] = 0;
    @memcpy(token_buf[0..token.len], token);
    token_buf[token.len] = 0;

    return .{
        .url = url_buf[0..url.len],
        .token = token_buf[0..token.len],
    };
}

fn hasWebSocketScheme(url: []const u8) bool {
    if (std.mem.startsWith(u8, url, "ws://")) return url.len > "ws://".len;
    if (std.mem.startsWith(u8, url, "wss://")) return url.len > "wss://".len;
    return false;
}

fn hasForbiddenByte(value: []const u8) bool {
    for (value) |c| {
        if (c <= 0x20 or c == 0x7f) return true;
    }
    return false;
}
