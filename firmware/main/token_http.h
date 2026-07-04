#pragma once
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// HTTP GET `url` into `out` (null-terminated). Returns the number of body bytes
// read (>= 0) on a 200 response, or a negative value on transport/HTTP error.
// Kept as a C shim so Zig never has to bind the large esp_http_client_config_t.
int token_http_get(const char *url, char *out, size_t out_size);

#ifdef __cplusplus
}
#endif
