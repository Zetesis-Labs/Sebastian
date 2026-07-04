//! Fetches a fresh LiveKit connection (server URL + access token) from the
//! Sebastian token server over HTTP, right before each session.
//!
//! Fetching per session (instead of embedding a static JWT) means the token is
//! always fresh — no 720h expiry, no reflash — and the server can embed an
//! explicit agent dispatch in it (see agent/token_server.py). The token server
//! URL is the only connection config left in secrets.zig.
//!
//! Response contract (plaintext, two lines):
//!   <serverUrl>\n<token>
//! Neither field contains a newline (wss:// URL, dot-separated JWT), so the
//! parse is a single split on the first '\n'.

const std = @import("std");
const c = @import("csdk.zig");

const log = std.log.scoped(.token);

extern fn token_http_get(url: [*:0]const u8, out: [*]u8, out_size: usize) c_int;

// JWTs run ~300-500 bytes; give generous headroom for URL + token + newline.
var response_buf: [1536]u8 = undefined;
var url_buf: [256]u8 = undefined;
var token_buf: [1280]u8 = undefined;
// Token-server URL, provisioned into NVS (see provisioning.c). Nul-terminated by
// nvs_get_str. No compiled default — the factory binary carries no config.
var token_server_url: [256]u8 = undefined;

pub const Connection = struct {
    server_url: [*:0]const u8,
    token: [*:0]const u8,
};

pub const Error = error{ HttpFailed, MalformedResponse };

/// GET the token server and parse the response into null-terminated URL + token.
/// The returned pointers reference module-static buffers valid until the next
/// fetch() — connect immediately, don't stash them across sessions.
pub fn fetch() Error!Connection {
    if (!c.sebastian_get_token_url(&token_server_url, token_server_url.len)) {
        log.err("no token server URL in NVS — device unprovisioned", .{});
        return error.HttpFailed;
    }
    const n = token_http_get(@ptrCast(&token_server_url), &response_buf, response_buf.len);
    if (n <= 0) {
        log.err("token server GET failed ({d})", .{n});
        return error.HttpFailed;
    }

    const body = response_buf[0..@intCast(n)];
    const nl = std.mem.indexOfScalar(u8, body, '\n') orelse {
        log.err("no newline in token response", .{});
        return error.MalformedResponse;
    };

    const url = std.mem.trim(u8, body[0..nl], " \r\n");
    const token = std.mem.trim(u8, body[nl + 1 ..], " \r\n");
    if (url.len == 0 or token.len == 0 or url.len >= url_buf.len or token.len >= token_buf.len) {
        log.err("token response fields out of range (url={d} token={d})", .{ url.len, token.len });
        return error.MalformedResponse;
    }

    @memcpy(url_buf[0..url.len], url);
    url_buf[url.len] = 0;
    @memcpy(token_buf[0..token.len], token);
    token_buf[token.len] = 0;

    log.info("token fetched ({d}B token) for {s}", .{ token.len, url_buf[0..url.len] });
    return .{
        .server_url = @ptrCast(&url_buf),
        .token = @ptrCast(&token_buf),
    };
}
