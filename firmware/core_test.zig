const std = @import("std");

const aec = @import("main/core/aec_core.zig");
const decimator = @import("main/core/decimator.zig");
const gate = @import("main/core/mic_gate.zig");
const pcm = @import("main/xvf_pcm.zig");
const pre_roll = @import("main/core/pre_roll_core.zig");
const token = @import("main/core/token_core.zig");

fn rawSample(sample: i32) i32 {
    const scale: i32 = @as(i32, 1) << pcm.SHIFT;
    return sample * scale;
}

fn setStereoPair(stereo: []i32, pair: usize, selected: i32, other: i32) void {
    const other_slot: usize = if (pcm.SLOT == 0) 1 else 0;
    stereo[pair * 2 + pcm.SLOT] = rawSample(selected);
    stereo[pair * 2 + other_slot] = rawSample(other);
}

fn fillStereo(stereo: []i32, selected: i32, other: i32) void {
    for (0..stereo.len / 2) |i| {
        setStereoPair(stereo, i, selected, other);
    }
}

fn noteSamples(state: *pre_roll.WindowState, samples: usize) void {
    for (0..samples) |_| state.noteSample();
}

test "pcm softClip is linear below knee and bounded above it" {
    try std.testing.expectEqual(@as(i16, 0), pcm.softClip(0));
    try std.testing.expectEqual(@as(i16, 1234), pcm.softClip(1234));
    try std.testing.expectEqual(@as(i16, -1234), pcm.softClip(-1234));
    try std.testing.expectEqual(@as(i16, 24000), pcm.softClip(24000));
    try std.testing.expectEqual(@as(i16, -24000), pcm.softClip(-24000));

    const high = pcm.softClip(1_000_000);
    const low = pcm.softClip(-1_000_000);
    try std.testing.expect(high > 24000);
    try std.testing.expect(high <= std.math.maxInt(i16));
    try std.testing.expect(low < -24000);
    try std.testing.expect(low >= std.math.minInt(i16));
    try std.testing.expect(@as(i32, high) + @as(i32, low) >= -1);
    try std.testing.expect(@as(i32, high) + @as(i32, low) <= 1);
}

test "pcm convert applies configured shift before clipping" {
    const raw: i32 = 16_384 * (1 << pcm.SHIFT);
    try std.testing.expectEqual(@as(i16, 16_384), pcm.convert(raw));
    try std.testing.expectEqual(@as(i16, -16_384), pcm.convert(-raw));
}

test "pre-roll header is stable little-endian SBPR" {
    const header = pre_roll.makeHeader(0x11223344, 0x55667788);

    try std.testing.expectEqualSlices(u8, "SBPR", header[0..4]);
    try std.testing.expectEqual(@as(u8, 1), header[4]);
    try std.testing.expectEqual(@as(u8, 0), header[5]);
    try std.testing.expectEqual(@as(u8, 0x80), header[6]);
    try std.testing.expectEqual(@as(u8, 0x3e), header[7]);
    try std.testing.expectEqual(@as(u8, 0x88), header[8]);
    try std.testing.expectEqual(@as(u8, 0x77), header[9]);
    try std.testing.expectEqual(@as(u8, 0x66), header[10]);
    try std.testing.expectEqual(@as(u8, 0x55), header[11]);
    try std.testing.expectEqual(@as(u8, 0x44), header[12]);
    try std.testing.expectEqual(@as(u8, 0x33), header[13]);
    try std.testing.expectEqual(@as(u8, 0x22), header[14]);
    try std.testing.expectEqual(@as(u8, 0x11), header[15]);
}

test "pre-roll window is anchored to wake mark" {
    const sr: usize = pre_roll.SAMPLE_RATE;
    const sr64: u64 = pre_roll.SAMPLE_RATE;
    const lead = pre_roll.WAKE_LEAD_SAMPLES;

    try std.testing.expectEqual(@as(usize, 0), pre_roll.windowSamples(0, 0, 0));
    try std.testing.expectEqual(sr, pre_roll.windowSamples(0, sr64, sr));
    try std.testing.expectEqual(sr, pre_roll.windowSamples(sr64 / 2, sr64, sr));
    try std.testing.expectEqual(lead + sr, pre_roll.windowSamples(10 * sr64, 11 * sr64, 11 * sr));
    try std.testing.expectEqual(pre_roll.SAMPLE_CAPACITY, pre_roll.windowSamples(10 * sr64, 30 * sr64, pre_roll.SAMPLE_CAPACITY));
}

test "pre-roll start index handles wrap and full ring" {
    try std.testing.expectEqual(@as(usize, 900), pre_roll.startIndex(1000, 100));

    const cap = pre_roll.SAMPLE_CAPACITY;
    try std.testing.expectEqual(@as(usize, cap - 20), pre_roll.startIndex(10, 30));
    try std.testing.expectEqual(@as(usize, 10), pre_roll.startIndex(10, cap));
}

test "pre-roll state tracks fill, wake and available duration" {
    var state = pre_roll.WindowState{};
    const sr: usize = pre_roll.SAMPLE_RATE;
    for (0..sr * 3) |_| state.noteSample();
    state.markWake();
    for (0..sr) |_| state.noteSample();

    try std.testing.expectEqual(pre_roll.WAKE_LEAD_SAMPLES + sr, state.windowSamples());
    try std.testing.expectEqual(@as(u32, 3000), state.availableMs());
}

test "pre-roll keeps wake anchored after long connect and ring wrap" {
    var state = pre_roll.WindowState{};
    const sr: usize = pre_roll.SAMPLE_RATE;

    noteSamples(&state, sr * 20);
    state.markWake();
    noteSamples(&state, sr * 8);

    try std.testing.expectEqual(sr * 10, state.windowSamples());
    try std.testing.expectEqual(@as(u32, 10_000), state.availableMs());
    try std.testing.expect(state.startIndex() < pre_roll.SAMPLE_CAPACITY);
}

test "pre-roll duration conversion floors partial milliseconds" {
    try std.testing.expectEqual(@as(u32, 0), pre_roll.availableMs(0));
    try std.testing.expectEqual(@as(u32, 0), pre_roll.availableMs(15));
    try std.testing.expectEqual(@as(u32, 999), pre_roll.availableMs(pre_roll.SAMPLE_RATE - 1));
    try std.testing.expectEqual(@as(u32, 1000), pre_roll.availableMs(pre_roll.SAMPLE_RATE));
    try std.testing.expectEqual(@as(u32, 12_000), pre_roll.availableMs(pre_roll.SAMPLE_CAPACITY));
}

test "pre-roll early wake only includes available lead" {
    var state = pre_roll.WindowState{};
    const sr: usize = pre_roll.SAMPLE_RATE;

    noteSamples(&state, sr);
    state.markWake();
    noteSamples(&state, sr / 4);

    try std.testing.expectEqual(sr + sr / 4, state.windowSamples());
    try std.testing.expectEqual(@as(usize, 0), state.startIndex());
    try std.testing.expectEqual(@as(u32, 1250), state.availableMs());
}

test "pre-roll late wake starts two seconds before wake mark" {
    var state = pre_roll.WindowState{};
    const sr: usize = pre_roll.SAMPLE_RATE;

    noteSamples(&state, sr * 5);
    state.markWake();
    noteSamples(&state, sr / 2);

    try std.testing.expectEqual(pre_roll.WAKE_LEAD_SAMPLES + sr / 2, state.windowSamples());
    try std.testing.expectEqual(sr * 3, state.startIndex());
}

test "pre-roll fill caps at ring capacity while write index keeps wrapping" {
    var state = pre_roll.WindowState{};

    noteSamples(&state, pre_roll.SAMPLE_CAPACITY + 123);

    try std.testing.expectEqual(pre_roll.SAMPLE_CAPACITY, state.filled);
    try std.testing.expectEqual(@as(usize, 123), state.write_idx);
    try std.testing.expectEqual(@as(u64, pre_roll.SAMPLE_CAPACITY + 123), state.total_written);
    try std.testing.expectEqual(@as(usize, 123), pre_roll.startIndex(state.write_idx, pre_roll.SAMPLE_CAPACITY));
}

test "mic gate half-duplex speaking and finite hangover" {
    var g = gate.Gate{};

    try std.testing.expect(g.setAgentSpeaking(true));
    try std.testing.expect(g.agentGateActive());
    try std.testing.expect(!g.setAgentSpeaking(true));

    try std.testing.expect(!g.setAgentSpeaking(false));
    for (0..gate.SPEAK_HANGOVER_FRAMES) |_| {
        try std.testing.expect(g.agentGateActive());
    }
    try std.testing.expect(!g.agentGateActive());
}

test "mic gate full-duplex bypasses agent gate but not mute" {
    var g = gate.Gate{};

    g.setFullDuplex(true);
    _ = g.setAgentSpeaking(true);
    try std.testing.expect(!g.agentGateActive());

    g.setMuted(true);
    try std.testing.expect(g.muted);
}

test "mic gate channel health classification catches dead and DC-pinned audio" {
    try std.testing.expect(gate.channelLooksBad(0, 0));
    try std.testing.expect(gate.channelLooksBad(400, 15001));
    try std.testing.expect(gate.channelLooksBad(400, -15001));
    try std.testing.expect(!gate.channelLooksBad(400, 15000));
    try std.testing.expect(!gate.channelLooksBad(400, -15000));
    try std.testing.expect(!gate.channelLooksBad(1400, 0));
}

test "mic gate repeated silence does not extend consumed hangover" {
    var g = gate.Gate{};

    try std.testing.expect(g.setAgentSpeaking(true));
    try std.testing.expect(!g.setAgentSpeaking(false));
    for (0..gate.SPEAK_HANGOVER_FRAMES) |_| {
        try std.testing.expect(g.agentGateActive());
    }

    try std.testing.expectEqual(@as(u32, 0), g.speak_hangover);
    try std.testing.expect(!g.agentGateActive());
    try std.testing.expect(!g.setAgentSpeaking(false));
    try std.testing.expect(!g.agentGateActive());
}

test "mic gate full-duplex preserves pending half-duplex hangover" {
    var g = gate.Gate{};

    _ = g.setAgentSpeaking(true);
    _ = g.setAgentSpeaking(false);
    try std.testing.expectEqual(gate.SPEAK_HANGOVER_FRAMES, g.speak_hangover);

    g.setFullDuplex(true);
    for (0..5) |_| {
        try std.testing.expect(!g.agentGateActive());
    }
    try std.testing.expectEqual(gate.SPEAK_HANGOVER_FRAMES, g.speak_hangover);

    g.setFullDuplex(false);
    try std.testing.expect(g.agentGateActive());
    try std.testing.expectEqual(gate.SPEAK_HANGOVER_FRAMES - 1, g.speak_hangover);
}

test "mic gate fresh bursts are reported after restart" {
    var g = gate.Gate{};

    try std.testing.expect(g.setAgentSpeaking(true));
    try std.testing.expect(!g.setAgentSpeaking(true));
    try std.testing.expect(!g.setAgentSpeaking(false));
    try std.testing.expect(g.setAgentSpeaking(true));
    try std.testing.expect(!g.setAgentSpeaking(true));
}

test "decimator emits 160 samples for one 10ms 48k frame" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var out = [_]i16{0} ** decimator.SAMPLES_16K;

    for (0..decimator.PAIRS_48K) |i| {
        stereo[i * 2] = 1000 << pcm.SHIFT;
        stereo[i * 2 + 1] = 1000 << pcm.SHIFT;
    }

    const result = d.decimate(stereo[0..], out[0..]);
    try std.testing.expectEqual(@as(usize, decimator.SAMPLES_16K), result.samples);
    try std.testing.expect(result.peak > 0);
    try std.testing.expect(result.mean > 0);
}

test "decimator handles partial frames without overrunning output" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (123 * 2);
    var out = [_]i16{0} ** decimator.SAMPLES_16K;

    for (0..123) |i| {
        stereo[i * 2] = @as(i32, @intCast(i)) << pcm.SHIFT;
        stereo[i * 2 + 1] = @as(i32, @intCast(i)) << pcm.SHIFT;
    }

    const result = d.decimate(stereo[0..], out[0..]);
    try std.testing.expectEqual(@as(usize, 41), result.samples);
}

test "decimator can process 960-pair barge-in frames in safe chunks" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (960 * 2);
    var out = [_]i16{0} ** decimator.SAMPLES_16K;
    var emitted: usize = 0;
    var off: usize = 0;

    while (off < 960) {
        const pairs: usize = @min(decimator.PAIRS_48K, 960 - off);
        const result = d.decimate(stereo[off * 2 .. (off + pairs) * 2], out[0..]);
        emitted += result.samples;
        off += pairs;
    }

    try std.testing.expectEqual(@as(usize, 320), emitted);
}

test "decimator reset makes identical chunks deterministic" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var out_a = [_]i16{0} ** decimator.SAMPLES_16K;
    var out_b = [_]i16{0} ** decimator.SAMPLES_16K;

    for (0..decimator.PAIRS_48K) |i| {
        const v: i32 = if (i == 20) 20_000 << pcm.SHIFT else 0;
        stereo[i * 2] = v;
        stereo[i * 2 + 1] = v;
    }

    const a = d.decimate(stereo[0..], out_a[0..]);
    d.reset();
    const b = d.decimate(stereo[0..], out_b[0..]);

    try std.testing.expectEqual(a.samples, b.samples);
    try std.testing.expectEqual(a.peak, b.peak);
    try std.testing.expectEqual(a.mean, b.mean);
    try std.testing.expectEqualSlices(i16, out_a[0..a.samples], out_b[0..b.samples]);
}

test "decimator preserves phase across uneven streaming chunks" {
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    for (0..decimator.PAIRS_48K) |i| {
        const selected: i32 = @as(i32, @intCast((i * 37) % 20_000)) - 10_000;
        const other: i32 = 7_000 - @as(i32, @intCast((i * 19) % 14_000));
        setStereoPair(stereo[0..], i, selected, other);
    }

    var full = decimator.Decimator{};
    var chunked = decimator.Decimator{};
    var full_out = [_]i16{0} ** decimator.SAMPLES_16K;
    var chunked_out = [_]i16{0} ** decimator.SAMPLES_16K;
    var scratch = [_]i16{0} ** decimator.SAMPLES_16K;

    const full_result = full.decimate(stereo[0..], full_out[0..]);

    const chunks = [_]usize{ 1, 2, 17, 319, 4, 56, 81 };
    var off: usize = 0;
    var emitted: usize = 0;
    for (chunks) |pairs| {
        const result = chunked.decimate(stereo[off * 2 .. (off + pairs) * 2], scratch[0..]);
        std.mem.copyForwards(i16, chunked_out[emitted .. emitted + result.samples], scratch[0..result.samples]);
        emitted += result.samples;
        off += pairs;
    }

    try std.testing.expectEqual(decimator.PAIRS_48K, off);
    try std.testing.expectEqual(full_result.samples, emitted);
    try std.testing.expectEqualSlices(i16, full_out[0..full_result.samples], chunked_out[0..emitted]);
}

test "decimator golden silence stays silent" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var out = [_]i16{123} ** decimator.SAMPLES_16K;

    const result = d.decimate(stereo[0..], out[0..]);

    try std.testing.expectEqual(@as(usize, decimator.SAMPLES_16K), result.samples);
    try std.testing.expectEqual(@as(i32, 0), result.peak);
    try std.testing.expectEqual(@as(i32, 0), result.mean);
    for (out[0..result.samples]) |sample| {
        try std.testing.expectEqual(@as(i16, 0), sample);
    }
}

test "decimator golden constant shows FIR warmup and unity gain" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var out = [_]i16{0} ** decimator.SAMPLES_16K;
    fillStereo(stereo[0..], 1000, -12000);

    const result = d.decimate(stereo[0..], out[0..]);

    try std.testing.expectEqual(@as(usize, decimator.SAMPLES_16K), result.samples);
    try std.testing.expectEqualSlices(i16, &[_]i16{ 2, -8, -19, 641, 1055, 994, 1000, 1000 }, out[0..8]);
    try std.testing.expectEqual(@as(i16, 1000), out[result.samples - 1]);
}

test "decimator golden impulse follows FIR taps at decimated positions" {
    var d = decimator.Decimator{};
    var stereo = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var out = [_]i16{0} ** decimator.SAMPLES_16K;
    setStereoPair(stereo[0..], 0, 16000, -3000);

    const result = d.decimate(stereo[0..], out[0..]);

    try std.testing.expectEqual(@as(usize, decimator.SAMPLES_16K), result.samples);
    try std.testing.expectEqualSlices(i16, &[_]i16{ 44, -212, 590, 4514, 590, -212, 44, 0 }, out[0..8]);
}

test "decimator ignores unselected stereo slot" {
    var clean_decim = decimator.Decimator{};
    var noisy_decim = decimator.Decimator{};
    var clean = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var noisy = [_]i32{0} ** (decimator.PAIRS_48K * 2);
    var clean_out = [_]i16{0} ** decimator.SAMPLES_16K;
    var noisy_out = [_]i16{0} ** decimator.SAMPLES_16K;

    for (0..decimator.PAIRS_48K) |i| {
        const selected: i32 = @as(i32, @intCast((i * 23) % 8_000)) - 4_000;
        const unrelated: i32 = if (i % 2 == 0) 20_000 else -20_000;
        setStereoPair(clean[0..], i, selected, 0);
        setStereoPair(noisy[0..], i, selected, unrelated);
    }

    const clean_result = clean_decim.decimate(clean[0..], clean_out[0..]);
    const noisy_result = noisy_decim.decimate(noisy[0..], noisy_out[0..]);

    try std.testing.expectEqual(clean_result.samples, noisy_result.samples);
    try std.testing.expectEqual(clean_result.peak, noisy_result.peak);
    try std.testing.expectEqual(clean_result.mean, noisy_result.mean);
    try std.testing.expectEqualSlices(i16, clean_out[0..clean_result.samples], noisy_out[0..noisy_result.samples]);
}

test "token parser accepts trimmed two-line response and nul-terminates fields" {
    var url_buf = [_]u8{0xaa} ** 32;
    var token_buf = [_]u8{0xaa} ** 64;

    const parsed = try token.parseResponse(" wss://lk.example \r\n jwt.token \r\n", &url_buf, &token_buf);

    try std.testing.expectEqualSlices(u8, "wss://lk.example", parsed.url);
    try std.testing.expectEqualSlices(u8, "jwt.token", parsed.token);
    try std.testing.expectEqual(@as(u8, 0), url_buf[parsed.url.len]);
    try std.testing.expectEqual(@as(u8, 0), token_buf[parsed.token.len]);
}

test "token parser rejects malformed and oversized responses" {
    var url_buf = [_]u8{0} ** 8;
    var token_buf = [_]u8{0} ** 8;

    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://lk.example", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("\nabc", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://x\n", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://too-long\njwt", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://x\njwt-too-long", &url_buf, &token_buf));
}

test "token parser accepts exact buffer limits before nul" {
    var url_buf = [_]u8{0xaa} ** 4;
    var token_buf = [_]u8{0xaa} ** 4;

    const parsed = try token.parseResponse("abc\nxyz", &url_buf, &token_buf);

    try std.testing.expectEqualSlices(u8, "abc", parsed.url);
    try std.testing.expectEqualSlices(u8, "xyz", parsed.token);
    try std.testing.expectEqual(@as(u8, 0), url_buf[3]);
    try std.testing.expectEqual(@as(u8, 0), token_buf[3]);

    try std.testing.expectError(error.MalformedResponse, token.parseResponse("abcd\nxyz", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("abc\nxyzz", &url_buf, &token_buf));
}

test "token parser rejects embedded line breaks but accepts trailing blanks" {
    var url_buf = [_]u8{0} ** 32;
    var token_buf = [_]u8{0} ** 32;

    const parsed = try token.parseResponse("wss://x\njwt\n\n", &url_buf, &token_buf);
    try std.testing.expectEqualSlices(u8, "wss://x", parsed.url);
    try std.testing.expectEqualSlices(u8, "jwt", parsed.token);

    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://x\rmore\njwt", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://x\njwt\rmore", &url_buf, &token_buf));
    try std.testing.expectError(error.MalformedResponse, token.parseResponse("wss://x\njwt\nextra", &url_buf, &token_buf));
}

test "aec scaled telemetry math never panics on bad floats" {
    try std.testing.expectEqual(@as(i32, -1), aec.scaled(null, 1000.0));
    try std.testing.expectEqual(@as(i32, -2), aec.scaled(std.math.nan(f32), 1000.0));
    try std.testing.expectEqual(std.math.maxInt(i32), aec.scaled(1.0e30, 1000.0));
    try std.testing.expectEqual(std.math.minInt(i32), aec.scaled(-1.0e30, 1000.0));
    try std.testing.expectEqual(@as(i32, 1234), aec.milli(1.234));
}

test "aec float comparisons reject NaN and honor epsilon" {
    try std.testing.expect(aec.floatsClose(1.0, 1.005, 0.01));
    try std.testing.expect(!aec.floatsClose(1.0, 1.02, 0.01));
    try std.testing.expect(!aec.floatsClose(std.math.nan(f32), 1.0, 0.01));
    try std.testing.expect(!aec.floatsClose(1.0, std.math.nan(f32), 0.01));
}

test "aec command encoding is little-endian and stable" {
    const i32_req = aec.makeI32Write(33, 90, 0x11223344);
    try std.testing.expectEqualSlices(u8, &[_]u8{ 33, 90, 4, 0x44, 0x33, 0x22, 0x11 }, i32_req[0..]);

    var f32_bytes: [4]u8 = undefined;
    aec.writeF32LE(&f32_bytes, 1.0);
    try std.testing.expectEqualSlices(u8, &[_]u8{ 0x00, 0x00, 0x80, 0x3f }, f32_bytes[0..]);
    try std.testing.expect(aec.floatsClose(aec.readF32LE(&f32_bytes), 1.0, 0.0));

    const pair_req = aec.makeF32PairWrite(33, 81, 1.0, -2.0);
    try std.testing.expectEqualSlices(u8, &[_]u8{
        33, 81, 8,
        0x00, 0x00, 0x80, 0x3f,
        0x00, 0x00, 0x00, 0xc0,
    }, pair_req[0..]);
}

test "aec signed i32 little-endian round trips boundary values" {
    var bytes: [4]u8 = undefined;

    aec.writeI32LE(&bytes, -2);
    try std.testing.expectEqualSlices(u8, &[_]u8{ 0xfe, 0xff, 0xff, 0xff }, bytes[0..]);
    try std.testing.expectEqual(@as(i32, -2), aec.readI32LE(&bytes));

    aec.writeI32LE(&bytes, std.math.maxInt(i32));
    try std.testing.expectEqual(@as(i32, std.math.maxInt(i32)), aec.readI32LE(&bytes));

    aec.writeI32LE(&bytes, std.math.minInt(i32));
    try std.testing.expectEqual(@as(i32, std.math.minInt(i32)), aec.readI32LE(&bytes));

    const req = aec.makeI32Write(7, 8, -2);
    try std.testing.expectEqualSlices(u8, &[_]u8{ 7, 8, 4, 0xfe, 0xff, 0xff, 0xff }, req[0..]);
}

test "aec float pair reader decodes little-endian command payloads" {
    const payload = [_]u8{
        0x00, 0x00, 0xc0, 0x3f,
        0x00, 0x00, 0x80, 0xbe,
    };
    const pair = aec.readF32PairLE(&payload);

    try std.testing.expect(aec.floatsClose(pair.first, 1.5, 0.0));
    try std.testing.expect(aec.floatsClose(pair.second, -0.25, 0.0));
}
