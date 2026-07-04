//! Board bring-up for the Seeed ReSpeaker XVF3800 + XIAO ESP32-S3.
//!
//! Audio topology:
//!   4-mic array → XMOS XVF3800 (AEC + beamforming + NS) ──I2S(DIN=43)──→ ESP32-S3
//!   ESP32-S3 ──I2S(DOUT=44)──→ TLV320AIC3104 → 5 W speaker / 3.5 mm AUX
//!
//! The XVF3800 (inthost firmware) is the I2S MASTER and clocks the bus at 48 kHz,
//! 32-bit stereo; the ESP32 is a SLAVE. To avoid a duplex-channel DMA corruption,
//! the ESP uses TWO SEPARATE I2S ports — RX (mic) on I2S_NUM_1 and TX (speaker)
//! on I2S_NUM_0 — sharing BCLK=8/WS=7 (MCLK unused). The AIC3104 (also a slave)
//! is configured over I2C for volume/routing only.

const std = @import("std");
const c = @import("csdk.zig");

const log = std.log.scoped(.board);

const I2C_SDA: c_int = 5;
const I2C_SCL: c_int = 6;
const I2S_MCLK: c_int = 9; // master clock reference for the XVF3800
const I2S_BCLK: c_int = 8;
const I2S_WS: c_int = 7;
const I2S_DOUT: c_int = 44; // ESP32 → AIC3104 (speaker)
const I2S_DIN: c_int = 43; // XVF3800 → ESP32 (mic)
const SAMPLE_RATE: u32 = 48000; // XVF3800 (I2S master) clocks the bus at 48 kHz
const AIC3104_ADDR: u16 = 0x18;

const Error = error{Esp};

var i2c_bus: c.i2c_master_bus_handle_t = null;
var i2s_tx: c.i2s_chan_handle_t = null;
var i2s_rx: c.i2s_chan_handle_t = null;
var play_dev: c.esp_codec_dev_handle_t = null;
var rec_dev: c.esp_codec_dev_handle_t = null;

/// Turn an `esp_err_t` into a Zig error, logging the IDF error name on failure.
fn ok(err: c.esp_err_t, comptime what: []const u8) Error!void {
    if (err != c.ESP_OK) {
        log.err("{s} failed: {s}", .{ what, std.mem.span(c.esp_err_to_name(err)) });
        return error.Esp;
    }
}

// --- I2C ---------------------------------------------------------------------

fn initI2c() Error!void {
    var cfg = std.mem.zeroes(c.i2c_master_bus_config_t);
    cfg.i2c_port = c.I2C_NUM_0;
    cfg.sda_io_num = I2C_SDA;
    cfg.scl_io_num = I2C_SCL;
    cfg.clk_source = c.I2C_CLK_SRC_DEFAULT;
    cfg.glitch_ignore_cnt = 7;
    cfg.flags.enable_internal_pullup = 1;
    try ok(c.i2c_new_master_bus(&cfg, &i2c_bus), "i2c_new_master_bus");
}

fn i2cScan() void {
    log.info("I2C scan (expect 0x18 AIC3104, 0x21 PCAL6416A, 0x2C XVF3800)", .{});
    var addr: u16 = 0x03;
    var found: u32 = 0;
    while (addr <= 0x77) : (addr += 1) {
        if (c.i2c_master_probe(i2c_bus, addr, 50) == c.ESP_OK) {
            log.info("  0x{X:0>2} ACK", .{addr});
            found += 1;
        }
    }
    log.info("I2C scan: {d} device(s)", .{found});
}

// --- AIC3104 (analog codec, speaker path) ------------------------------------

fn aicWrite(dev: c.i2c_master_dev_handle_t, reg: u8, val: u8) Error!void {
    const buf = [_]u8{ reg, val };
    try ok(c.i2c_master_transmit(dev, &buf, buf.len, 100), "aic3104 write");
}

fn initAic3104() Error!void {
    var dev_cfg = std.mem.zeroes(c.i2c_device_config_t);
    dev_cfg.dev_addr_length = c.I2C_ADDR_BIT_LEN_7;
    dev_cfg.device_address = AIC3104_ADDR;
    dev_cfg.scl_speed_hz = 100000;

    var dev: c.i2c_master_dev_handle_t = null;
    try ok(c.i2c_master_bus_add_device(i2c_bus, &dev_cfg, &dev), "aic3104 add device");

    try aicWrite(dev, 0x00, 0x00); // page 0
    try aicWrite(dev, 0x2B, 0x00); // left DAC volume 0 dB
    try aicWrite(dev, 0x2C, 0x00); // right DAC volume 0 dB
    try aicWrite(dev, 0x33, 0x0D); // HPLOUT 0 dB, unmuted
    try aicWrite(dev, 0x41, 0x0D); // HPROUT 0 dB, unmuted
    try aicWrite(dev, 0x56, 0x0B); // left line-out
    try aicWrite(dev, 0x5D, 0x0B); // right line-out
    log.info("AIC3104 configured", .{});
}

// --- I2S (duplex, master, 16 kHz, 32-bit stereo, no MCLK) --------------------

fn stdCfg() c.i2s_std_config_t {
    var std_cfg = std.mem.zeroes(c.i2s_std_config_t);
    std_cfg.clk_cfg.sample_rate_hz = SAMPLE_RATE;
    std_cfg.clk_cfg.clk_src = c.I2S_CLK_SRC_DEFAULT;
    std_cfg.clk_cfg.mclk_multiple = c.I2S_MCLK_MULTIPLE_256;
    std_cfg.slot_cfg.data_bit_width = c.I2S_DATA_BIT_WIDTH_32BIT;
    std_cfg.slot_cfg.slot_bit_width = c.I2S_SLOT_BIT_WIDTH_AUTO;
    std_cfg.slot_cfg.slot_mode = c.I2S_SLOT_MODE_STEREO;
    std_cfg.slot_cfg.slot_mask = c.I2S_STD_SLOT_BOTH;
    std_cfg.slot_cfg.ws_width = 32;
    std_cfg.slot_cfg.bit_shift = true; // Philips standard I2S
    std_cfg.gpio_cfg.mclk = c.I2S_GPIO_UNUSED;
    std_cfg.gpio_cfg.bclk = I2S_BCLK;
    std_cfg.gpio_cfg.ws = I2S_WS;
    return std_cfg;
}

// Separate I2S ports so the mic RX (read by a dedicated task) and the speaker TX
// (driven by av_render) never share one duplex channel — sharing it corrupts the
// DMA and crashes. Both are I2S slaves to the XVF3800's shared BCLK/WS clock.
fn initI2s() Error!void {
    // --- TX (speaker) on I2S_NUM_0 ---
    var tx_chan = std.mem.zeroes(c.i2s_chan_config_t);
    tx_chan.id = c.I2S_NUM_0;
    tx_chan.role = c.I2S_ROLE_SLAVE;
    tx_chan.dma_desc_num = 6;
    tx_chan.dma_frame_num = 240;
    tx_chan.auto_clear_after_cb = true;
    try ok(c.i2s_new_channel(&tx_chan, &i2s_tx, null), "i2s_new_channel tx");

    var tx_cfg = stdCfg();
    tx_cfg.gpio_cfg.dout = I2S_DOUT;
    tx_cfg.gpio_cfg.din = c.I2S_GPIO_UNUSED;
    try ok(c.i2s_channel_init_std_mode(i2s_tx, &tx_cfg), "i2s tx init");
    try ok(c.i2s_channel_enable(i2s_tx), "i2s tx enable");

    // --- RX (mic) on I2S_NUM_1, sharing BCLK/WS as inputs ---
    var rx_chan = std.mem.zeroes(c.i2s_chan_config_t);
    rx_chan.id = c.I2S_NUM_1;
    rx_chan.role = c.I2S_ROLE_SLAVE;
    rx_chan.dma_desc_num = 6;
    rx_chan.dma_frame_num = 240;
    try ok(c.i2s_new_channel(&rx_chan, null, &i2s_rx), "i2s_new_channel rx");

    var rx_cfg = stdCfg();
    rx_cfg.gpio_cfg.dout = c.I2S_GPIO_UNUSED;
    rx_cfg.gpio_cfg.din = I2S_DIN;
    try ok(c.i2s_channel_init_std_mode(i2s_rx, &rx_cfg), "i2s rx init");
    try ok(c.i2s_channel_enable(i2s_rx), "i2s rx enable");
}

// --- esp_codec_dev playback device -------------------------------------------
// The AIC3104 is configured directly over I2C and the XVF3800 self-configures, so
// esp_codec_dev only needs to move I2S data. A no-op codec_if satisfies its API.

fn noopOpen(_: *const c.audio_codec_if_t, _: ?*anyopaque, _: c_int) callconv(.c) c_int {
    return c.ESP_CODEC_DEV_OK;
}
fn noopIsOpen(_: *const c.audio_codec_if_t) callconv(.c) bool {
    return true;
}
fn noopEnable(_: *const c.audio_codec_if_t, _: bool) callconv(.c) c_int {
    return c.ESP_CODEC_DEV_OK;
}
fn noopSetFs(_: *const c.audio_codec_if_t, _: *c.esp_codec_dev_sample_info_t) callconv(.c) c_int {
    return c.ESP_CODEC_DEV_OK;
}
fn noopClose(_: *const c.audio_codec_if_t) callconv(.c) c_int {
    return c.ESP_CODEC_DEV_OK;
}

const noop_codec_if = c.audio_codec_if_t{
    .open = noopOpen,
    .is_open = noopIsOpen,
    .enable = noopEnable,
    .set_fs = noopSetFs,
    // set_vol deliberately absent: esp_codec_dev only creates its SOFTWARE
    // volume stage when the codec lacks set_vol. With a no-op set_vol here the
    // volume knob silently did nothing (playback was always full-scale PCM).
    .close = noopClose,
};

fn initPlayback() Error!void {
    var i2s_cfg = std.mem.zeroes(c.audio_codec_i2s_cfg_t);
    i2s_cfg.port = 0;
    i2s_cfg.tx_handle = i2s_tx;
    const data_if = c.audio_codec_new_i2s_data(&i2s_cfg) orelse return error.Esp;

    var dev_cfg = std.mem.zeroes(c.esp_codec_dev_cfg_t);
    dev_cfg.dev_type = c.ESP_CODEC_DEV_TYPE_OUT;
    dev_cfg.codec_if = &noop_codec_if;
    dev_cfg.data_if = data_if;
    play_dev = c.esp_codec_dev_new(&dev_cfg) orelse return error.Esp;
    // The historical 35 (and every previous value) never did anything: the
    // noop set_vol suppressed esp_codec_dev's software volume, so playback was
    // always full-scale. Now that the knob is real, 100 = 0 dB reproduces the
    // loudness the device has always had. The pre-open call returns
    // NOT_SUPPORT but stores the value; open() re-applies it via sw-vol.
    _ = c.esp_codec_dev_set_out_vol(play_dev, 100);
    log.info("Playback codec device ready", .{});
}

fn initRecord() Error!void {
    var i2s_cfg = std.mem.zeroes(c.audio_codec_i2s_cfg_t);
    i2s_cfg.port = 0;
    i2s_cfg.rx_handle = i2s_rx;
    const data_if = c.audio_codec_new_i2s_data(&i2s_cfg) orelse return error.Esp;

    var dev_cfg = std.mem.zeroes(c.esp_codec_dev_cfg_t);
    dev_cfg.dev_type = c.ESP_CODEC_DEV_TYPE_IN;
    dev_cfg.codec_if = &noop_codec_if;
    dev_cfg.data_if = data_if;
    rec_dev = c.esp_codec_dev_new(&dev_cfg) orelse return error.Esp;
    log.info("Record codec device ready", .{});
}

// --- Public API --------------------------------------------------------------

pub fn init() Error!void {
    log.info("Init ReSpeaker XVF3800 + XIAO ESP32-S3", .{});
    try initI2c();
    i2cScan();
    try initAic3104();
    try initI2s();
    try initPlayback();
    try initRecord();
    log.info("Board init complete", .{});
}

pub fn playbackHandle() c.esp_codec_dev_handle_t {
    return play_dev;
}
pub fn recordHandle() c.esp_codec_dev_handle_t {
    return rec_dev;
}

pub fn i2sTx() c.i2s_chan_handle_t {
    return i2s_tx;
}
pub fn i2sRx() c.i2s_chan_handle_t {
    return i2s_rx;
}
pub fn i2cBus() c.i2c_master_bus_handle_t {
    return i2c_bus;
}
