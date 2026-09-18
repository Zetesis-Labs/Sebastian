//! Opens a LiveKit session for this unit: POST /v1/sessions on the control
//! room, authenticated with the device secret (fleet block 1, RF-12). The
//! server answers with the LiveKit URL and a fresh access token that carries
//! the agent dispatch, so nothing static is embedded in the firmware.
//!
//! The control room's origin is derived from the provisioned token URL — the
//! only server URL the unit stores.

const std = @import("std");
const c = @import("csdk.zig");
const control = @import("control.zig");
const url_core = @import("core/url_core.zig");

const log = std.log.scoped(.token);

// JWTs run ~300-500 bytes; give generous headroom.
var url_buf: [256]u8 = undefined;
var token_buf: [1280]u8 = undefined;
var token_server_url: [256]u8 = undefined;
var origin_z: [256]u8 = undefined;
var secret_buf: [129]u8 = undefined;
var id_z: [13]u8 = undefined;

pub const Connection = struct {
    server_url: [*:0]const u8,
    token: [*:0]const u8,
};

pub const Error = error{ HttpFailed, Unprovisioned };

/// Open a session and return the LiveKit URL + token as null-terminated
/// strings. They reference module-static buffers valid until the next fetch()
/// — connect immediately, don't stash them across sessions.
pub fn fetch() Error!Connection {
    if (!c.sebastian_get_token_url(&token_server_url, token_server_url.len)) {
        log.err("no control room URL in NVS — device unprovisioned", .{});
        return error.Unprovisioned;
    }
    if (!c.sebastian_get_device_secret(&secret_buf, secret_buf.len) or !control.deviceId(&id_z)) {
        log.err("no device secret — the unit cannot open sessions until adopted", .{});
        return error.Unprovisioned;
    }
    const o = url_core.origin(std.mem.sliceTo(&token_server_url, 0));
    const origin = std.fmt.bufPrintZ(&origin_z, "{s}", .{o}) catch return error.HttpFailed;
    const rc = c.sebastian_session_create(origin.ptr, @ptrCast(&id_z), @ptrCast(&secret_buf), &url_buf, url_buf.len, &token_buf, token_buf.len);
    if (rc != 0) {
        log.err("POST /v1/sessions failed (rc={d}) — is this unit adopted by {s}?", .{ rc, origin });
        return error.HttpFailed;
    }
    log.info("session created as {s} ({d}B token) for {s}", .{ std.mem.sliceTo(&id_z, 0), std.mem.sliceTo(&token_buf, 0).len, std.mem.sliceTo(&url_buf, 0) });
    return .{ .server_url = @ptrCast(&url_buf), .token = @ptrCast(&token_buf) };
}
