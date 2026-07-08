//! Per-unit configuration for a Sebastian device.
//!
//! Two tiers:
//!  - The `pub var` fields below are DEFAULTS that `load()` overrides at boot from
//!    NVS (provisioned over serial by the web installer — see provisioning.c). This
//!    is what the installer's "mode" selects: full/half-duplex, fixed beam +
//!    azimuth, and the boot self-tests. Changing them is a re-provision, not a
//!    reflash.
//!  - The `pub const` fields (mic_channel) are resolved at COMPILE time — they feed
//!    comptime slot/shift selection in xvf_pcm.zig, so they still need a reflash.
//!
//! `load()` must run before any of these are read (app_main calls it right after
//! the provisioning receiver starts, before the XVF/AEC config is applied).

// NVS-backed config getters, implemented in provisioning.c. Missing keys return
// the supplied default, so an unprovisioned unit keeps the defaults below.
extern fn sebastian_cfg_get_bool(key: [*:0]const u8, def: bool) bool;
extern fn sebastian_cfg_get_i32(key: [*:0]const u8, def: i32) i32;

/// Which XVF3800 output slot the mic capture consumes.
///
/// - `.right` = raw ASR beam. Post-AEC + beamforming, but SKIPS the post-processing
///   (no noise suppression, no residual/non-linear echo suppression). Cleanest for a
///   single downstream NS pass (e.g. the agent's BVC).
/// - `.left`  = comms beam. Full post-processing: de-reverb + NS + residual-echo
///   suppression + limiter. The channel designed for full-duplex calls (the XVF
///   suppresses its own loudspeaker echo). Louder (AGC + limiter) → needs more shift.
pub const MicChannel = enum { left, right };

/// The channel this unit is installed with. COMPILE-TIME: xvf_pcm.zig derives the
/// I2S slot + gain shift from it at comptime, so changing it needs a reflash.
///
/// .left since 2026-07-08 (path B, ROADMAP §5): on the self-hosted SFU there is
/// no cloud BVC, so the raw ASR beam reaches the model with NO noise suppression
/// and NO residual-echo cleanup — full-duplex feeds the agent its own voice back
/// ("se acopla") and noisy rooms degrade transcription. The comms beam provides
/// both on-chip (validated: probeDualChannel echo-rise −2 568 adaptive/unconverged
/// vs +101 655 on raw), and with BVC gone the old "double NS = tinny" concern no
/// longer applies — comms is now the single NS pass by design.
pub const mic_channel: MicChannel = .left;

/// Freeze the XVF beamformer to a fixed direction so the AEC can converge.
///
/// The adaptive beamformer tracks the talker, which continuously changes the
/// mic→echo transfer function — a non-stationary target the AEC can never lock
/// (confirmed on hardware: adaptive → AECCONVERGED stays 0 forever, filter jitters
/// at ~0.003; fixed → converges in ~1s, filter ~0.025 at a stable tap). A fixed
/// beam trades talker tracking for a working AEC, the prerequisite for dropping
/// half-duplex and running full-duplex. Full-duplex mode provisions this true;
/// half-duplex provisions it false (an adaptive beam that tracks the talker).
pub var fixed_beam: bool = true;

/// Fixed-beam azimuth in degrees. 0 = front (mic-array reference axis),
/// positive = counter-clockwise. Only used when `fixed_beam` is true.
pub var fixed_beam_azimuth_deg: f32 = 0.0;

/// Full-duplex: keep the mic live while the agent speaks instead of gating it to
/// silence (half-duplex). Only safe once the AEC actually cancels the loudspeaker
/// echo — otherwise the agent hears itself as phantom user turns. Pair with
/// `fixed_beam = true` (the AEC only converges with a fixed beam). When false,
/// the half-duplex gate + wake-word barge-in handle the echo.
pub var full_duplex: bool = true;

/// Endpoint mode (ROADMAP §9): hold ONE persistent LiveKit session instead of
/// connecting per wake. At idle the mic publishes gate silence (Opus DTX ≈ zero
/// network) and the wake word runs on-device exactly as before — no voice leaves
/// the device until wake. What changes: the server can push audio ANYTIME
/// (proactive announce, music), and the wake→response path skips token+connect
/// (~1-2 s faster, no per-wake connect failures). The agent pairs this with a
/// lazy LLM session (opens on the `sebastian.session` "wake" signal).
pub var always_connected: bool = false;

// The three boot self-tests stay COMPILE-TIME (`const`), NOT runtime `var`. As a
// runtime var they defeat dead-code elimination, so xvf_aec's probe machinery —
// including its ~15 KB of static tone/rx buffers (TONE_PAIRS, tone_buf,
// probe_rx_buf) — is kept in internal RAM even in production. Those 15 KB starve
// the TLS hardware-AES DMA allocation (`esp-aes: Failed to allocate memory`) and
// LiveKit can't connect. Keeping them const lets the compiler elide the probes +
// their buffers. Enabling a probe is therefore a reflash, not a re-provision —
// which is fine: it's a bring-up/diagnostics-only mode.

/// Run the AEC convergence self-test at boot (xvf_aec.probeReference): plays a
/// session-level tone through the speaker and reports whether the AEC converges,
/// with no session or human needed — the result lands in Grafana after a reset.
/// Diagnostic only; leave false in production (it beeps for ~10s at boot).
pub const probe_aec_on_boot: bool = false;

/// Dual-channel echo test at boot (xvf_aec.probeDualChannel): plays agent-like
/// noise and compares residual echo on the comms (LEFT) vs raw ASR (RIGHT) beams,
/// with the beam adaptive (worst case) and fixed (reference). Answers whether the
/// comms channel's non-linear suppressor enables full-duplex WITH tracking.
/// Diagnostic only; leave false in production (plays ~12s of noise at boot).
pub const probe_dual_channel_on_boot: bool = false;

/// Output-gain actuator test at boot (xvf_aec.probeOutputGain): plays noise at
/// FAR_EXTGAIN 0 dB vs −12 dB and compares pre-AEC mic echo. Answers whether
/// FAR_EXTGAIN is a usable master volume for echo-headroom auto-leveling.
/// Diagnostic only; leave false in production (~7s of noise at boot).
pub const probe_output_gain_on_boot: bool = false;

/// Override the defaults above with the values provisioned into NVS. Call once at
/// boot, before the config is read (XVF/AEC apply, boot self-tests). Each missing
/// key keeps its default, so a factory/unprovisioned unit is unchanged.
pub fn load() void {
    fixed_beam = sebastian_cfg_get_bool("fixed_beam", fixed_beam);
    fixed_beam_azimuth_deg = @floatFromInt(sebastian_cfg_get_i32("beam_az", @intFromFloat(fixed_beam_azimuth_deg)));
    full_duplex = sebastian_cfg_get_bool("full_duplex", full_duplex);
    always_connected = sebastian_cfg_get_bool("always_conn", always_connected);
}
