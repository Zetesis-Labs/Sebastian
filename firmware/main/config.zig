//! Install-time configuration for a Sebastian unit.
//!
//! These are comptime constants: set them before flashing a unit and rebuild.
//! The selection is resolved at compile time, so there is zero runtime cost.

/// Which XVF3800 output slot the mic capture consumes.
///
/// - `.right` = raw ASR beam. Post-AEC + beamforming, but SKIPS the post-processing
///   (no noise suppression, no residual/non-linear echo suppression). Cleanest for a
///   single downstream NS pass (e.g. the agent's BVC).
/// - `.left`  = comms beam. Full post-processing: de-reverb + NS + residual-echo
///   suppression + limiter. The channel designed for full-duplex calls (the XVF
///   suppresses its own loudspeaker echo). Louder (AGC + limiter) → needs more shift.
pub const MicChannel = enum { left, right };

/// The channel this unit is installed with. Change per install and reflash.
pub const mic_channel: MicChannel = .right;

/// Freeze the XVF beamformer to a fixed direction so the AEC can converge.
///
/// The adaptive beamformer tracks the talker, which continuously changes the
/// mic→echo transfer function — a non-stationary target the AEC can never lock
/// (confirmed on hardware: adaptive → AECCONVERGED stays 0 forever, filter jitters
/// at ~0.003; fixed → converges in ~1s, filter ~0.025 at a stable tap). A fixed
/// beam trades talker tracking for a working AEC, the prerequisite for dropping
/// half-duplex and running full-duplex. Point the azimuth at where the talker
/// sits relative to the unit and reflash per install.
pub const fixed_beam: bool = true;

/// Fixed-beam azimuth in degrees. 0 = front (mic-array reference axis),
/// positive = counter-clockwise. Only used when `fixed_beam` is true.
pub const fixed_beam_azimuth_deg: f32 = 0.0;

/// Full-duplex: keep the mic live while the agent speaks instead of gating it to
/// silence (half-duplex). Only safe once the AEC actually cancels the loudspeaker
/// echo — otherwise the agent hears itself as phantom user turns. Pair with
/// `fixed_beam = true` (the AEC only converges with a fixed beam). When false,
/// the half-duplex gate + wake-word barge-in handle the echo.
pub const full_duplex: bool = true;

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
