//! URL helpers for the control-plane client — pure, host-tested.
//!
//! The device only provisions ONE server URL (the token endpoint). The
//! control-plane endpoints live on the same origin, so the client derives
//! `scheme://host[:port]` from it instead of provisioning a second URL.

const std = @import("std");

/// `scheme://host[:port]` prefix of `url` — the input up to (excluding) the
/// first path slash. Inputs without a scheme or path come back unchanged.
pub fn origin(url: []const u8) []const u8 {
    const scheme_end = std.mem.indexOf(u8, url, "://") orelse return url;
    const rest = url[scheme_end + 3 ..];
    const slash = std.mem.indexOfScalar(u8, rest, '/') orelse return url;
    return url[0 .. scheme_end + 3 + slash];
}
