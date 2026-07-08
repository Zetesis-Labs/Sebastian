//! Hand-written `extern` bindings to the ESP-IDF C ABI.
//!
//! We bind the C symbols directly instead of `@cImport`, so Zig never parses the
//! ESP-IDF/newlib headers (translate-c chokes on them for the Xtensa target). The
//! C side is compiled and linked by ESP-IDF; here we only declare the exact
//! layouts and prototypes we call. Struct layouts and enum values are transcribed
//! from the IDF v5.4 headers for esp32s3 (SOC_I2S_HW_VERSION_2). Keep in sync.

pub const esp_err_t = c_int;
pub const ESP_OK: esp_err_t = 0;

// --- Logging -----------------------------------------------------------------
pub const ESP_LOG_ERROR: c_uint = 1;
pub const ESP_LOG_INFO: c_uint = 3;
pub const ESP_LOG_WARN: c_uint = 2;
pub const ESP_LOG_DEBUG: c_uint = 4;
pub extern fn esp_log_write(level: c_uint, tag: [*:0]const u8, format: [*:0]const u8, ...) void;
pub extern fn esp_log_level_set(tag: [*:0]const u8, level: c_uint) void;
pub extern fn esp_err_to_name(code: esp_err_t) [*:0]const u8;

// --- FreeRTOS ----------------------------------------------------------------
pub extern fn vTaskDelay(ticks: u32) void;
pub extern fn vTaskDelete(task: ?*anyopaque) void; // null = delete calling task
pub const TaskFunction_t = ?*const fn (?*anyopaque) callconv(.c) void;
pub const TSK_NO_AFFINITY: c_int = 0x7FFFFFFF;
pub extern fn xTaskCreatePinnedToCore(code: TaskFunction_t, name: [*:0]const u8, stack_depth: u32, params: ?*anyopaque, prio: u32, handle: ?*anyopaque, core_id: c_int) c_int;

// --- I2C (esp_driver_i2c / i2c_master) ---------------------------------------
pub const i2c_master_bus_handle_t = ?*anyopaque;
pub const i2c_master_dev_handle_t = ?*anyopaque;

pub const I2C_NUM_0: c_int = 0;
pub const I2C_CLK_SRC_DEFAULT: c_int = 11; // SOC_MOD_CLK_XTAL (esp32s3)
pub const I2C_ADDR_BIT_LEN_7: c_int = 0;

pub const i2c_master_bus_config_t = extern struct {
    i2c_port: c_int,
    sda_io_num: c_int,
    scl_io_num: c_int,
    clk_source: c_int,
    glitch_ignore_cnt: u8,
    intr_priority: c_int,
    trans_queue_depth: usize,
    flags: packed struct(u32) {
        enable_internal_pullup: u1 = 0,
        allow_pd: u1 = 0,
        _pad: u30 = 0,
    },
};

pub const i2c_device_config_t = extern struct {
    dev_addr_length: c_int,
    device_address: u16,
    scl_speed_hz: u32,
    scl_wait_us: u32,
    flags: packed struct(u32) {
        disable_ack_check: u1 = 0,
        _pad: u31 = 0,
    },
};

pub extern fn i2c_new_master_bus(cfg: *const i2c_master_bus_config_t, ret: *i2c_master_bus_handle_t) esp_err_t;
pub extern fn i2c_master_bus_add_device(bus: i2c_master_bus_handle_t, cfg: *const i2c_device_config_t, ret: *i2c_master_dev_handle_t) esp_err_t;
pub extern fn i2c_master_transmit(dev: i2c_master_dev_handle_t, buf: [*]const u8, size: usize, timeout_ms: c_int) esp_err_t;
pub extern fn i2c_master_receive(dev: i2c_master_dev_handle_t, buf: [*]u8, size: usize, timeout_ms: c_int) esp_err_t;
pub extern fn i2c_master_probe(bus: i2c_master_bus_handle_t, address: u16, timeout_ms: c_int) esp_err_t;

// --- I2S (esp_driver_i2s / i2s_std) ------------------------------------------
pub const i2s_chan_handle_t = ?*anyopaque;

pub const I2S_NUM_0: c_int = 0;
pub const I2S_NUM_1: c_int = 1;
pub const I2S_ROLE_MASTER: c_int = 0;
pub const I2S_ROLE_SLAVE: c_int = 1;
pub const I2S_DATA_BIT_WIDTH_32BIT: c_int = 32;
pub const I2S_SLOT_MODE_STEREO: c_int = 2;
pub const I2S_SLOT_BIT_WIDTH_AUTO: c_int = 0;
pub const I2S_MCLK_MULTIPLE_256: c_int = 256;
pub const I2S_STD_SLOT_BOTH: c_int = 3; // BIT(0) | BIT(1)
pub const I2S_CLK_SRC_DEFAULT: c_int = 6; // SOC_MOD_CLK_PLL_F160M (esp32s3)
pub const I2S_GPIO_UNUSED: c_int = -1;

pub const i2s_chan_config_t = extern struct {
    id: c_int,
    role: c_int,
    dma_desc_num: u32,
    dma_frame_num: u32,
    auto_clear_after_cb: bool,
    auto_clear_before_cb: bool,
    allow_pd: bool,
    intr_priority: c_int,
};

pub const i2s_std_clk_config_t = extern struct {
    sample_rate_hz: u32,
    clk_src: c_int,
    ext_clk_freq_hz: u32,
    mclk_multiple: c_int,
};

pub const i2s_std_slot_config_t = extern struct {
    data_bit_width: c_int,
    slot_bit_width: c_int,
    slot_mode: c_int,
    slot_mask: c_int,
    ws_width: u32,
    ws_pol: bool,
    bit_shift: bool,
    left_align: bool,
    big_endian: bool,
    bit_order_lsb: bool,
};

pub const i2s_std_gpio_config_t = extern struct {
    mclk: c_int,
    bclk: c_int,
    ws: c_int,
    dout: c_int,
    din: c_int,
    invert_flags: packed struct(u32) {
        mclk_inv: u1 = 0,
        bclk_inv: u1 = 0,
        ws_inv: u1 = 0,
        _pad: u29 = 0,
    },
};

pub const i2s_std_config_t = extern struct {
    clk_cfg: i2s_std_clk_config_t,
    slot_cfg: i2s_std_slot_config_t,
    gpio_cfg: i2s_std_gpio_config_t,
};

pub extern fn i2s_new_channel(cfg: *const i2s_chan_config_t, tx: ?*i2s_chan_handle_t, rx: ?*i2s_chan_handle_t) esp_err_t;
pub extern fn i2s_channel_init_std_mode(handle: i2s_chan_handle_t, cfg: *const i2s_std_config_t) esp_err_t;
pub extern fn i2s_channel_enable(handle: i2s_chan_handle_t) esp_err_t;
pub extern fn i2s_channel_read(handle: i2s_chan_handle_t, dest: *anyopaque, size: usize, bytes_read: *usize, timeout_ms: u32) esp_err_t;
pub extern fn i2s_channel_write(handle: i2s_chan_handle_t, src: *const anyopaque, size: usize, bytes_written: *usize, timeout_ms: u32) esp_err_t;
pub extern fn i2s_channel_disable(handle: i2s_chan_handle_t) esp_err_t;
pub extern fn abort() noreturn;

// =============================================================================
// Milestone 2 — LiveKit audio (render path: agent voice → speaker)
// =============================================================================

// --- esp_codec_dev -----------------------------------------------------------
pub const esp_codec_dev_handle_t = ?*anyopaque;
pub const audio_codec_data_if_t = anyopaque;

pub const ESP_CODEC_DEV_TYPE_IN: c_int = 1;
pub const ESP_CODEC_DEV_TYPE_OUT: c_int = 2;
pub const ESP_CODEC_DEV_OK: c_int = 0;

pub const esp_codec_dev_sample_info_t = extern struct {
    bits_per_sample: u8,
    channel: u8,
    channel_mask: u16,
    sample_rate: u32,
    mclk_multiple: c_int,
};

pub const audio_codec_if_t = extern struct {
    open: ?*const fn (*const audio_codec_if_t, ?*anyopaque, c_int) callconv(.c) c_int = null,
    is_open: ?*const fn (*const audio_codec_if_t) callconv(.c) bool = null,
    enable: ?*const fn (*const audio_codec_if_t, bool) callconv(.c) c_int = null,
    set_fs: ?*const fn (*const audio_codec_if_t, *esp_codec_dev_sample_info_t) callconv(.c) c_int = null,
    mute: ?*const fn (*const audio_codec_if_t, bool) callconv(.c) c_int = null,
    set_vol: ?*const fn (*const audio_codec_if_t, f32) callconv(.c) c_int = null,
    set_mic_gain: ?*const fn (*const audio_codec_if_t, f32) callconv(.c) c_int = null,
    set_mic_channel_gain: ?*const fn (*const audio_codec_if_t, u16, f32) callconv(.c) c_int = null,
    mute_mic: ?*const fn (*const audio_codec_if_t, bool) callconv(.c) c_int = null,
    set_reg: ?*const fn (*const audio_codec_if_t, c_int, c_int) callconv(.c) c_int = null,
    get_reg: ?*const fn (*const audio_codec_if_t, c_int, *c_int) callconv(.c) c_int = null,
    dump_reg: ?*const fn (*const audio_codec_if_t) callconv(.c) void = null,
    close: ?*const fn (*const audio_codec_if_t) callconv(.c) c_int = null,
};

pub const audio_codec_i2s_cfg_t = extern struct {
    port: u8,
    rx_handle: ?*anyopaque = null,
    tx_handle: ?*anyopaque = null,
};

pub const esp_codec_dev_cfg_t = extern struct {
    dev_type: c_int,
    codec_if: ?*const audio_codec_if_t,
    data_if: ?*const audio_codec_data_if_t,
};

pub extern fn audio_codec_new_i2s_data(cfg: *audio_codec_i2s_cfg_t) ?*const audio_codec_data_if_t;
pub extern fn esp_codec_dev_new(cfg: *esp_codec_dev_cfg_t) esp_codec_dev_handle_t;
pub extern fn esp_codec_dev_set_out_vol(dev: esp_codec_dev_handle_t, volume: c_int) c_int;

// --- av_render / esp_capture -------------------------------------------------
pub const audio_render_handle_t = ?*anyopaque;
pub const av_render_handle_t = ?*anyopaque;
pub const esp_capture_handle_t = ?*anyopaque;

pub const i2s_render_cfg_t = extern struct {
    play_handle: esp_codec_dev_handle_t,
    cb: ?*const anyopaque = null,
    fixed_clock: bool = false,
    ctx: ?*anyopaque = null,
};

pub const av_render_cfg_t = extern struct {
    audio_render: audio_render_handle_t,
    video_render: ?*anyopaque = null,
    sync_mode: c_int = 0,
    audio_raw_fifo_size: u32 = 0,
    video_raw_fifo_size: u32 = 0,
    audio_render_fifo_size: u32 = 0,
    video_render_fifo_size: u32 = 0,
    quit_when_eos: bool = false,
    allow_drop_data: bool = false,
    pause_render_only: bool = false,
    pause_on_first_frame: bool = false,
    ctx: ?*anyopaque = null,
    video_cvt_in_render: bool = false,
};

pub const av_render_audio_frame_info_t = extern struct {
    channel: u8,
    bits_per_sample: u8,
    sample_rate: u32,
};

pub extern fn av_render_alloc_i2s_render(cfg: *i2s_render_cfg_t) audio_render_handle_t;
pub extern fn av_render_open(cfg: *av_render_cfg_t) av_render_handle_t;
pub extern fn av_render_set_fixed_frame_info(render: av_render_handle_t, info: *av_render_audio_frame_info_t) c_int;
pub extern fn av_render_flush(render: av_render_handle_t) c_int;
pub extern fn esp_audio_dec_register_default() c_int;
pub extern fn esp_audio_enc_register_default() c_int;

// --- LiveKit room ------------------------------------------------------------
pub const livekit_room_handle_t = ?*anyopaque;
pub const livekit_data_stream_handle_t = ?*anyopaque;
pub const LIVEKIT_ERR_NONE: c_int = 0;
pub const LIVEKIT_MEDIA_TYPE_NONE: c_int = 0;
pub const LIVEKIT_MEDIA_TYPE_AUDIO: c_int = 1;
pub const LIVEKIT_AUDIO_CODEC_OPUS: c_int = 3;

pub const livekit_video_encode_options_t = extern struct {
    codec: c_int,
    width: c_int,
    height: c_int,
    fps: c_int,
};
pub const livekit_audio_encode_options_t = extern struct {
    codec: c_int,
    sample_rate: u32,
    channel_count: u8,
};
pub const livekit_pub_options_t = extern struct {
    kind: c_int,
    video_encode: livekit_video_encode_options_t,
    audio_encode: livekit_audio_encode_options_t,
    capturer: esp_capture_handle_t,
};
pub const livekit_sub_options_t = extern struct {
    kind: c_int,
    renderer: av_render_handle_t,
};
pub const livekit_participant_info_t = extern struct {
    sid: ?[*:0]const u8,
    identity: ?[*:0]const u8,
    name: ?[*:0]const u8,
    metadata: ?[*:0]const u8,
    kind: c_int,
    state: c_int,
};
pub const livekit_data_stream_options_t = extern struct {
    topic: [*:0]const u8,
    is_text: bool = false,
    total_length: u64 = 0,
    has_total_length: bool = false,
};
pub const livekit_data_payload_t = extern struct {
    bytes: ?[*]u8,
    size: usize,
};
pub const livekit_data_received_t = extern struct {
    payload: livekit_data_payload_t,
    topic: ?[*:0]u8,
    sender_identity: ?[*:0]u8,
};
pub const livekit_room_options_t = extern struct {
    publish: livekit_pub_options_t,
    subscribe: livekit_sub_options_t,
    on_state_changed: ?*const fn (c_int, ?*anyopaque) callconv(.c) void = null,
    on_rpc_result: ?*const anyopaque = null,
    on_data_received: ?*const fn (*const livekit_data_received_t, ?*anyopaque) callconv(.c) void = null,
    on_room_info: ?*const anyopaque = null,
    on_participant_info: ?*const fn (*const livekit_participant_info_t, ?*anyopaque) callconv(.c) void = null,
    ctx: ?*anyopaque = null,
};

pub const livekit_data_publish_options_t = extern struct {
    payload: *livekit_data_payload_t,
    topic: [*:0]const u8,
    lossy: bool = false,
    destination_identities: ?[*][*:0]const u8 = null,
    destination_identities_count: c_int = 0,
};
pub extern fn livekit_room_publish_data(handle: livekit_room_handle_t, options: *const livekit_data_publish_options_t) c_int;

pub extern fn livekit_system_init() c_int;
pub extern fn livekit_room_create(handle: *livekit_room_handle_t, options: *const livekit_room_options_t) c_int;
pub extern fn livekit_room_connect(handle: livekit_room_handle_t, server_url: [*:0]const u8, token: [*:0]const u8) c_int;
pub extern fn livekit_room_close(handle: livekit_room_handle_t) c_int;
pub extern fn livekit_room_destroy(handle: livekit_room_handle_t) c_int;
pub extern fn livekit_room_get_state(handle: livekit_room_handle_t) c_int;
pub extern fn livekit_connection_state_str(state: c_int) [*:0]const u8;
pub extern fn livekit_room_data_stream_open(handle: livekit_room_handle_t, options: *const livekit_data_stream_options_t, stream: *livekit_data_stream_handle_t) c_int;
pub extern fn livekit_room_data_stream_write(handle: livekit_room_handle_t, stream: livekit_data_stream_handle_t, data: [*]const u8, size: usize) c_int;
pub extern fn livekit_room_data_stream_close(handle: livekit_room_handle_t, stream: livekit_data_stream_handle_t) c_int;
pub const LIVEKIT_CONNECTION_STATE_DISCONNECTED: c_int = 0;
pub const LIVEKIT_CONNECTION_STATE_CONNECTED: c_int = 2;
pub const LIVEKIT_CONNECTION_STATE_FAILED: c_int = 4;
pub const LIVEKIT_PARTICIPANT_KIND_AGENT: c_int = 4;
pub const LIVEKIT_PARTICIPANT_STATE_ACTIVE: c_int = 2;

// --- heap capabilities -------------------------------------------------------
pub const MALLOC_CAP_DMA: u32 = 1 << 3;
pub const MALLOC_CAP_8BIT: u32 = 1 << 2;
pub const MALLOC_CAP_SPIRAM: u32 = 1 << 10;
pub const MALLOC_CAP_INTERNAL: u32 = 1 << 11;
pub extern fn heap_caps_malloc(size: usize, caps: u32) ?*anyopaque;
pub extern fn heap_caps_get_free_size(caps: u32) usize;
pub extern fn heap_caps_get_largest_free_block(caps: u32) usize;

// --- WiFi (example_utils) + SNTP ---------------------------------------------
pub extern fn lk_example_network_connect() bool;
// NVS-backed WiFi + serial provisioning (main/provisioning.c). net_connect uses
// provisioned creds, falling back to the compiled default.
pub extern fn sebastian_net_connect() bool;
pub extern fn sebastian_provisioning_start() void;
pub extern fn sebastian_get_token_url(out: [*]u8, out_size: usize) bool;
pub extern fn esp_sntp_setoperatingmode(mode: u8) void;
pub extern fn esp_sntp_setservername(idx: u8, server: [*:0]const u8) void;
pub extern fn esp_sntp_init() void;

// --- esp_capture (mic path: your voice → agent) ------------------------------
pub const esp_capture_audio_src_if_t = anyopaque;
pub const ESP_CAPTURE_SYNC_MODE_AUDIO: c_int = 2;

pub const esp_capture_audio_aec_src_cfg_t = extern struct {
    mic_layout: ?[*:0]const u8 = null,
    record_handle: ?*anyopaque = null,
    channel: u8 = 0,
    channel_mask: u8 = 0,
    data_on_vad: bool = false,
};

pub const esp_capture_cfg_t = extern struct {
    sync_mode: c_int = 0,
    audio_src: ?*esp_capture_audio_src_if_t = null,
    video_src: ?*anyopaque = null,
};

pub extern fn esp_capture_new_audio_aec_src(cfg: *esp_capture_audio_aec_src_cfg_t) ?*esp_capture_audio_src_if_t;
pub extern fn esp_capture_open(cfg: *esp_capture_cfg_t, capture: *esp_capture_handle_t) c_int;

// Plain device source (no on-chip AEC; the XVF3800 already does AEC in hardware).
pub const esp_capture_audio_dev_src_cfg_t = extern struct {
    record_handle: esp_codec_dev_handle_t,
};
pub extern fn esp_capture_new_audio_dev_src(cfg: *esp_capture_audio_dev_src_cfg_t) ?*esp_capture_audio_src_if_t;

// --- WiFi power save (off, for real-time audio stability) --------------------
pub const WIFI_PS_NONE: c_int = 0;
pub extern fn esp_wifi_set_ps(ps_type: c_int) esp_err_t;

// --- mic diagnostics (per-channel peak) ---
pub extern fn esp_codec_dev_open(dev: esp_codec_dev_handle_t, fs: *esp_codec_dev_sample_info_t) c_int;
pub extern fn esp_codec_dev_read(dev: esp_codec_dev_handle_t, data: *anyopaque, len: c_int) c_int;
pub extern fn esp_codec_dev_close(dev: esp_codec_dev_handle_t) c_int;
pub extern fn esp_timer_get_time() i64;
pub extern fn esp_restart() noreturn;

pub const I2S_DATA_BIT_WIDTH_16BIT: c_int = 16;
pub const I2S_SLOT_BIT_WIDTH_32BIT: c_int = 32;

// --- custom esp_capture audio source (mic_src.zig) ---------------------------
// dev_src opens the codec in mono without a channel_mask, so the I2S mono read
// lands on the LEFT slot (silence). The XVF3800 puts the processed voice on the
// RIGHT slot, so we need a source that opens with channel_mask = RIGHT.
pub const ESP_CAPTURE_ERR_OK: c_int = 0;
pub const ESP_CAPTURE_ERR_NOT_SUPPORTED: c_int = -3;
pub const ESP_CAPTURE_ERR_INTERNAL: c_int = -8;
pub const ESP_CAPTURE_FMT_ID_PCM: c_uint = 0x204D4350; // 4CC 'P','C','M',' '
pub const I2S_STD_SLOT_RIGHT: u16 = 2; // BIT(1)

pub const esp_capture_audio_info_t = extern struct {
    format_id: c_uint = 0,
    sample_rate: u32 = 0,
    channel: u8 = 0,
    bits_per_sample: u8 = 0,
};

pub const esp_capture_stream_frame_t = extern struct {
    stream_type: c_uint = 0,
    pts: u32 = 0,
    data: [*c]u8 = null,
    size: c_int = 0,
};

pub const esp_capture_audio_src_iface_t = extern struct {
    open: ?*const fn (*esp_capture_audio_src_iface_t) callconv(.c) c_int = null,
    get_support_codecs: ?*const fn (*esp_capture_audio_src_iface_t, *[*c]const c_uint, *u8) callconv(.c) c_int = null,
    set_fixed_caps: ?*const fn (*esp_capture_audio_src_iface_t, *const esp_capture_audio_info_t) callconv(.c) c_int = null,
    negotiate_caps: ?*const fn (*esp_capture_audio_src_iface_t, *esp_capture_audio_info_t, *esp_capture_audio_info_t) callconv(.c) c_int = null,
    start: ?*const fn (*esp_capture_audio_src_iface_t) callconv(.c) c_int = null,
    read_frame: ?*const fn (*esp_capture_audio_src_iface_t, *esp_capture_stream_frame_t) callconv(.c) c_int = null,
    stop: ?*const fn (*esp_capture_audio_src_iface_t) callconv(.c) c_int = null,
    close: ?*const fn (*esp_capture_audio_src_iface_t) callconv(.c) c_int = null,
};
