const std = @import("std");

// Builds main/app.zig into a single object that ESP-IDF links into the `main`
// component. Include paths and target macros are harvested from the ESP-IDF
// component graph by cmake/zig.cmake and passed as '|'-joined options, which
// keeps this version-agnostic (no hardcoded IDF include list, no file I/O).
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});
    const includes = b.option([]const u8, "includes", "'|'-separated include dirs (-I)") orelse "";
    const system_includes = b.option([]const u8, "system_includes", "'|'-separated system include dirs (-isystem, e.g. toolchain libc)") orelse "";
    const defines = b.option([]const u8, "defines", "'|'-separated C macros (NAME or NAME=value)") orelse "";

    const mod = b.createModule(.{
        .root_source_file = b.path("main/app.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true,
    });

    var inc = std.mem.tokenizeScalar(u8, includes, '|');
    while (inc.next()) |dir| {
        if (dir.len == 0) continue;
        mod.addIncludePath(.{ .cwd_relative = dir });
    }

    var sinc = std.mem.tokenizeScalar(u8, system_includes, '|');
    while (sinc.next()) |dir| {
        if (dir.len == 0) continue;
        mod.addSystemIncludePath(.{ .cwd_relative = dir });
    }

    var def = std.mem.tokenizeScalar(u8, defines, '|');
    while (def.next()) |macro| {
        if (macro.len == 0) continue;
        if (std.mem.indexOfScalar(u8, macro, '=')) |eq| {
            mod.addCMacro(macro[0..eq], macro[eq + 1 ..]);
        } else {
            mod.addCMacro(macro, "1");
        }
    }

    const obj = b.addObject(.{ .name = "app_zig", .root_module = mod });
    const install = b.addInstallArtifact(obj, .{
        .dest_dir = .{ .override = .{ .custom = "obj" } },
    });
    b.getInstallStep().dependOn(&install.step);
}
