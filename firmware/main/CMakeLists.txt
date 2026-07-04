# The application lives in Zig (app.zig). placeholder.c keeps the component
# non-empty for ESP-IDF; cmake/zig.cmake compiles app.zig and links app_main.
# token_http.c is a small C shim so Zig avoids binding esp_http_client_config_t.
# provisioning.c backs WiFi with NVS + a serial config receiver (web installer).
idf_component_register(
    SRCS "placeholder.c" "token_http.c" "provisioning.c"
    INCLUDE_DIRS "."
    REQUIRES livekit__livekit livekit__sandbox_token livekit__example_utils espressif__esp_codec_dev esp_driver_i2c esp_driver_i2s nvs_flash esp_netif esp_wifi esp_event esp_http_client json esp_driver_usb_serial_jtag mww
)

include("${CMAKE_SOURCE_DIR}/cmake/zig.cmake")
