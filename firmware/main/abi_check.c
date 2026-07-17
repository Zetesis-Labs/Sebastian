// ABI layout guards — C half.
//
// csdk.zig transcribes these C layouts by hand (Zig never parses the headers),
// so an IDF or component upgrade that changes a field, its order or its
// alignment would break the Zig side SILENTLY at runtime. This TU asserts the
// real headers against the same literals the Zig comptime block asserts in
// csdk.zig. A failed _Static_assert here means the headers drifted; a failed
// assert in csdk.zig means the Zig declaration did. Keep both in sync.
//
// Layouts assume the ESP32-S3 ILP32 ABI (pointers/size_t 4 bytes, enums 4,
// u64 8-aligned). Literals verified against IDF v5.4 and the vendored
// components in managed_components/ (2026-07).

#include <stddef.h>
#include <stdint.h>

#include "driver/i2c_master.h"
#include "driver/i2s_common.h"
#include "driver/i2s_std.h"
#include "esp_heap_caps.h"
#include "esp_wifi.h"

#include "esp_codec_dev.h"
#include "esp_codec_dev_defaults.h"

#include "av_render.h"
#include "av_render_types.h"
#include "av_render_default.h"

#include "livekit.h"
#include "livekit_data_stream.h"

#include "esp_capture.h"
#include "esp_capture_types.h"
#include "esp_capture_audio_src_if.h"
#include "impl/esp_capture_audio_aec_src.h"
#include "impl/esp_capture_audio_dev_src.h"

#define CHECK_SIZE(type, bytes) _Static_assert(sizeof(type) == (bytes), #type " size drifted — sync csdk.zig")
#define CHECK_OFF(type, field, bytes) _Static_assert(offsetof(type, field) == (bytes), #type "." #field " offset drifted — sync csdk.zig")
#define CHECK_VAL(name, value) _Static_assert((name) == (value), #name " value drifted — sync csdk.zig")

// --- IDF esp_driver_i2c ---
CHECK_SIZE(i2c_master_bus_config_t, 32);
CHECK_OFF(i2c_master_bus_config_t, i2c_port, 0);
CHECK_OFF(i2c_master_bus_config_t, sda_io_num, 4);
CHECK_OFF(i2c_master_bus_config_t, scl_io_num, 8);
CHECK_OFF(i2c_master_bus_config_t, clk_source, 12);
CHECK_OFF(i2c_master_bus_config_t, glitch_ignore_cnt, 16);
CHECK_OFF(i2c_master_bus_config_t, intr_priority, 20);
CHECK_OFF(i2c_master_bus_config_t, trans_queue_depth, 24);
CHECK_OFF(i2c_master_bus_config_t, flags, 28);
CHECK_SIZE(i2c_device_config_t, 20);
CHECK_OFF(i2c_device_config_t, dev_addr_length, 0);
CHECK_OFF(i2c_device_config_t, device_address, 4);
CHECK_OFF(i2c_device_config_t, scl_speed_hz, 8);
CHECK_OFF(i2c_device_config_t, scl_wait_us, 12);
CHECK_OFF(i2c_device_config_t, flags, 16);
CHECK_VAL(I2C_NUM_0, 0);
CHECK_VAL(I2C_CLK_SRC_DEFAULT, 11); // SOC_MOD_CLK_XTAL on esp32s3
CHECK_VAL(I2C_ADDR_BIT_LEN_7, 0);

// --- IDF esp_driver_i2s ---
CHECK_SIZE(i2s_chan_config_t, 24);
CHECK_OFF(i2s_chan_config_t, id, 0);
CHECK_OFF(i2s_chan_config_t, role, 4);
CHECK_OFF(i2s_chan_config_t, dma_desc_num, 8);
CHECK_OFF(i2s_chan_config_t, dma_frame_num, 12);
CHECK_OFF(i2s_chan_config_t, auto_clear_after_cb, 16);
CHECK_OFF(i2s_chan_config_t, auto_clear_before_cb, 17);
CHECK_OFF(i2s_chan_config_t, allow_pd, 18);
CHECK_OFF(i2s_chan_config_t, intr_priority, 20);
CHECK_SIZE(i2s_std_clk_config_t, 16);
CHECK_OFF(i2s_std_clk_config_t, sample_rate_hz, 0);
CHECK_OFF(i2s_std_clk_config_t, clk_src, 4);
CHECK_OFF(i2s_std_clk_config_t, ext_clk_freq_hz, 8);
CHECK_OFF(i2s_std_clk_config_t, mclk_multiple, 12);
CHECK_SIZE(i2s_std_slot_config_t, 28);
CHECK_OFF(i2s_std_slot_config_t, data_bit_width, 0);
CHECK_OFF(i2s_std_slot_config_t, slot_bit_width, 4);
CHECK_OFF(i2s_std_slot_config_t, slot_mode, 8);
CHECK_OFF(i2s_std_slot_config_t, slot_mask, 12);
CHECK_OFF(i2s_std_slot_config_t, ws_width, 16);
CHECK_OFF(i2s_std_slot_config_t, ws_pol, 20);
CHECK_OFF(i2s_std_slot_config_t, bit_shift, 21);
CHECK_OFF(i2s_std_slot_config_t, left_align, 22);
CHECK_OFF(i2s_std_slot_config_t, big_endian, 23);
CHECK_OFF(i2s_std_slot_config_t, bit_order_lsb, 24);
CHECK_SIZE(i2s_std_gpio_config_t, 24);
CHECK_OFF(i2s_std_gpio_config_t, mclk, 0);
CHECK_OFF(i2s_std_gpio_config_t, bclk, 4);
CHECK_OFF(i2s_std_gpio_config_t, ws, 8);
CHECK_OFF(i2s_std_gpio_config_t, dout, 12);
CHECK_OFF(i2s_std_gpio_config_t, din, 16);
CHECK_OFF(i2s_std_gpio_config_t, invert_flags, 20);
CHECK_SIZE(i2s_std_config_t, 68);
CHECK_OFF(i2s_std_config_t, clk_cfg, 0);
CHECK_OFF(i2s_std_config_t, slot_cfg, 16);
CHECK_OFF(i2s_std_config_t, gpio_cfg, 44);
CHECK_VAL(I2S_ROLE_MASTER, 0);
CHECK_VAL(I2S_ROLE_SLAVE, 1);
CHECK_VAL(I2S_DATA_BIT_WIDTH_16BIT, 16);
CHECK_VAL(I2S_DATA_BIT_WIDTH_32BIT, 32);
CHECK_VAL(I2S_SLOT_BIT_WIDTH_AUTO, 0);
CHECK_VAL(I2S_SLOT_BIT_WIDTH_32BIT, 32);
CHECK_VAL(I2S_SLOT_MODE_STEREO, 2);
CHECK_VAL(I2S_MCLK_MULTIPLE_256, 256);
CHECK_VAL(I2S_STD_SLOT_RIGHT, 2);  // BIT(1)
CHECK_VAL(I2S_STD_SLOT_BOTH, 3);   // BIT(0) | BIT(1)
CHECK_VAL(I2S_CLK_SRC_DEFAULT, 6); // SOC_MOD_CLK_PLL_F160M on esp32s3
CHECK_VAL(I2S_GPIO_UNUSED, -1);

// --- esp_codec_dev ---
CHECK_SIZE(esp_codec_dev_sample_info_t, 12);
CHECK_OFF(esp_codec_dev_sample_info_t, bits_per_sample, 0);
CHECK_OFF(esp_codec_dev_sample_info_t, channel, 1);
CHECK_OFF(esp_codec_dev_sample_info_t, channel_mask, 2);
CHECK_OFF(esp_codec_dev_sample_info_t, sample_rate, 4);
CHECK_OFF(esp_codec_dev_sample_info_t, mclk_multiple, 8);
CHECK_SIZE(audio_codec_if_t, 52);
CHECK_OFF(audio_codec_if_t, open, 0);
CHECK_OFF(audio_codec_if_t, close, 48);
CHECK_SIZE(audio_codec_i2s_cfg_t, 12);
CHECK_OFF(audio_codec_i2s_cfg_t, port, 0);
CHECK_OFF(audio_codec_i2s_cfg_t, rx_handle, 4);
CHECK_OFF(audio_codec_i2s_cfg_t, tx_handle, 8);
CHECK_SIZE(esp_codec_dev_cfg_t, 12);
CHECK_OFF(esp_codec_dev_cfg_t, dev_type, 0);
CHECK_OFF(esp_codec_dev_cfg_t, codec_if, 4);
CHECK_OFF(esp_codec_dev_cfg_t, data_if, 8);
CHECK_VAL(ESP_CODEC_DEV_TYPE_IN, 1);
CHECK_VAL(ESP_CODEC_DEV_TYPE_OUT, 2);
CHECK_VAL(ESP_CODEC_DEV_OK, 0);

// --- av_render ---
CHECK_SIZE(i2s_render_cfg_t, 16);
CHECK_OFF(i2s_render_cfg_t, play_handle, 0);
CHECK_OFF(i2s_render_cfg_t, cb, 4);
CHECK_OFF(i2s_render_cfg_t, fixed_clock, 8);
CHECK_OFF(i2s_render_cfg_t, ctx, 12);
CHECK_SIZE(av_render_cfg_t, 40);
CHECK_OFF(av_render_cfg_t, audio_render, 0);
CHECK_OFF(av_render_cfg_t, video_render, 4);
CHECK_OFF(av_render_cfg_t, sync_mode, 8);
CHECK_OFF(av_render_cfg_t, audio_raw_fifo_size, 12);
CHECK_OFF(av_render_cfg_t, video_raw_fifo_size, 16);
CHECK_OFF(av_render_cfg_t, audio_render_fifo_size, 20);
CHECK_OFF(av_render_cfg_t, video_render_fifo_size, 24);
CHECK_OFF(av_render_cfg_t, quit_when_eos, 28);
CHECK_OFF(av_render_cfg_t, allow_drop_data, 29);
CHECK_OFF(av_render_cfg_t, pause_render_only, 30);
CHECK_OFF(av_render_cfg_t, pause_on_first_frame, 31);
CHECK_OFF(av_render_cfg_t, ctx, 32);
CHECK_OFF(av_render_cfg_t, video_cvt_in_render, 36);
CHECK_SIZE(av_render_audio_frame_info_t, 8);
CHECK_OFF(av_render_audio_frame_info_t, channel, 0);
CHECK_OFF(av_render_audio_frame_info_t, bits_per_sample, 1);
CHECK_OFF(av_render_audio_frame_info_t, sample_rate, 4);

// --- LiveKit SDK ---
CHECK_SIZE(livekit_video_encode_options_t, 16);
CHECK_OFF(livekit_video_encode_options_t, codec, 0);
CHECK_OFF(livekit_video_encode_options_t, width, 4);
CHECK_OFF(livekit_video_encode_options_t, height, 8);
CHECK_OFF(livekit_video_encode_options_t, fps, 12);
CHECK_SIZE(livekit_audio_encode_options_t, 12);
CHECK_OFF(livekit_audio_encode_options_t, codec, 0);
CHECK_OFF(livekit_audio_encode_options_t, sample_rate, 4);
CHECK_OFF(livekit_audio_encode_options_t, channel_count, 8);
CHECK_SIZE(livekit_pub_options_t, 36);
CHECK_OFF(livekit_pub_options_t, kind, 0);
CHECK_OFF(livekit_pub_options_t, video_encode, 4);
CHECK_OFF(livekit_pub_options_t, audio_encode, 20);
CHECK_OFF(livekit_pub_options_t, capturer, 32);
CHECK_SIZE(livekit_sub_options_t, 8);
CHECK_OFF(livekit_sub_options_t, kind, 0);
CHECK_OFF(livekit_sub_options_t, renderer, 4);
CHECK_SIZE(livekit_participant_info_t, 24);
CHECK_OFF(livekit_participant_info_t, sid, 0);
CHECK_OFF(livekit_participant_info_t, identity, 4);
CHECK_OFF(livekit_participant_info_t, name, 8);
CHECK_OFF(livekit_participant_info_t, metadata, 12);
CHECK_OFF(livekit_participant_info_t, kind, 16);
CHECK_OFF(livekit_participant_info_t, state, 20);
// total_length also pins the 8-byte alignment of u64 on Xtensa.
CHECK_SIZE(livekit_data_stream_options_t, 24);
CHECK_OFF(livekit_data_stream_options_t, topic, 0);
CHECK_OFF(livekit_data_stream_options_t, is_text, 4);
CHECK_OFF(livekit_data_stream_options_t, total_length, 8);
CHECK_OFF(livekit_data_stream_options_t, has_total_length, 16);
CHECK_SIZE(livekit_data_payload_t, 8);
CHECK_OFF(livekit_data_payload_t, bytes, 0);
CHECK_OFF(livekit_data_payload_t, size, 4);
CHECK_SIZE(livekit_data_received_t, 16);
CHECK_OFF(livekit_data_received_t, payload, 0);
CHECK_OFF(livekit_data_received_t, topic, 8);
CHECK_OFF(livekit_data_received_t, sender_identity, 12);
CHECK_SIZE(livekit_room_options_t, 68);
CHECK_OFF(livekit_room_options_t, publish, 0);
CHECK_OFF(livekit_room_options_t, subscribe, 36);
CHECK_OFF(livekit_room_options_t, on_state_changed, 44);
CHECK_OFF(livekit_room_options_t, on_rpc_result, 48);
CHECK_OFF(livekit_room_options_t, on_data_received, 52);
CHECK_OFF(livekit_room_options_t, on_room_info, 56);
CHECK_OFF(livekit_room_options_t, on_participant_info, 60);
CHECK_OFF(livekit_room_options_t, ctx, 64);
CHECK_SIZE(livekit_data_publish_options_t, 20);
CHECK_OFF(livekit_data_publish_options_t, payload, 0);
CHECK_OFF(livekit_data_publish_options_t, topic, 4);
CHECK_OFF(livekit_data_publish_options_t, lossy, 8);
CHECK_OFF(livekit_data_publish_options_t, destination_identities, 12);
CHECK_OFF(livekit_data_publish_options_t, destination_identities_count, 16);
CHECK_VAL(LIVEKIT_ERR_NONE, 0);
CHECK_VAL(LIVEKIT_MEDIA_TYPE_NONE, 0);
CHECK_VAL(LIVEKIT_MEDIA_TYPE_AUDIO, 1);
CHECK_VAL(LIVEKIT_AUDIO_CODEC_OPUS, 3);
CHECK_VAL(LIVEKIT_CONNECTION_STATE_DISCONNECTED, 0);
CHECK_VAL(LIVEKIT_CONNECTION_STATE_CONNECTED, 2);
CHECK_VAL(LIVEKIT_CONNECTION_STATE_FAILED, 4);
CHECK_VAL(LIVEKIT_PARTICIPANT_KIND_AGENT, 4);
CHECK_VAL(LIVEKIT_PARTICIPANT_STATE_ACTIVE, 2);

// --- esp_capture (note: C names the iface esp_capture_audio_src_if_t) ---
CHECK_SIZE(esp_capture_audio_aec_src_cfg_t, 12);
CHECK_OFF(esp_capture_audio_aec_src_cfg_t, mic_layout, 0);
CHECK_OFF(esp_capture_audio_aec_src_cfg_t, record_handle, 4);
CHECK_OFF(esp_capture_audio_aec_src_cfg_t, channel, 8);
CHECK_OFF(esp_capture_audio_aec_src_cfg_t, channel_mask, 9);
CHECK_OFF(esp_capture_audio_aec_src_cfg_t, data_on_vad, 10);
CHECK_SIZE(esp_capture_cfg_t, 12);
CHECK_OFF(esp_capture_cfg_t, sync_mode, 0);
CHECK_OFF(esp_capture_cfg_t, audio_src, 4);
CHECK_OFF(esp_capture_cfg_t, video_src, 8);
CHECK_SIZE(esp_capture_audio_dev_src_cfg_t, 4);
CHECK_OFF(esp_capture_audio_dev_src_cfg_t, record_handle, 0);
CHECK_SIZE(esp_capture_audio_info_t, 12);
CHECK_OFF(esp_capture_audio_info_t, format_id, 0);
CHECK_OFF(esp_capture_audio_info_t, sample_rate, 4);
CHECK_OFF(esp_capture_audio_info_t, channel, 8);
CHECK_OFF(esp_capture_audio_info_t, bits_per_sample, 9);
CHECK_SIZE(esp_capture_stream_frame_t, 16);
CHECK_OFF(esp_capture_stream_frame_t, stream_type, 0);
CHECK_OFF(esp_capture_stream_frame_t, pts, 4);
CHECK_OFF(esp_capture_stream_frame_t, data, 8);
CHECK_OFF(esp_capture_stream_frame_t, size, 12);
CHECK_SIZE(esp_capture_audio_src_if_t, 32);
CHECK_OFF(esp_capture_audio_src_if_t, open, 0);
CHECK_OFF(esp_capture_audio_src_if_t, close, 28);
CHECK_VAL(ESP_CAPTURE_SYNC_MODE_AUDIO, 2);
CHECK_VAL(ESP_CAPTURE_ERR_OK, 0);
CHECK_VAL(ESP_CAPTURE_ERR_NOT_SUPPORTED, -3);
CHECK_VAL(ESP_CAPTURE_ERR_INTERNAL, -8);
CHECK_VAL(ESP_CAPTURE_FMT_ID_PCM, 0x204D4350); // 4CC 'P','C','M',' '

// --- heap capabilities / WiFi power save ---
CHECK_VAL(MALLOC_CAP_DMA, 1 << 3);
CHECK_VAL(MALLOC_CAP_8BIT, 1 << 2);
CHECK_VAL(MALLOC_CAP_SPIRAM, 1 << 10);
CHECK_VAL(MALLOC_CAP_INTERNAL, 1 << 11);
CHECK_VAL(WIFI_PS_NONE, 0);
