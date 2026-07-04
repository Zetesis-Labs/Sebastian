#pragma once
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

// Load the TFLite model and configure the audio frontend + inference engine.
// model_data/model_len: raw .tflite flatbuffer bytes
// probability_cutoff: score threshold per frame (0..1)
// window_size: consecutive frames above cutoff to confirm detection
// Returns false if allocation or setup fails.
bool mww_init(const uint8_t *model_data, size_t model_len,
              float probability_cutoff, int window_size);

// Feed 160 samples of 16kHz mono int16 PCM (one 10ms chunk).
// Internally accumulates 2 feature frames then runs one inference.
// Returns true when wake word is confirmed (sliding window satisfied).
// Automatically resets detection state after returning true.
bool mww_feed(const int16_t *pcm_16k, int num_samples);

// Reset frontend state and sliding window (call before each new detection session).
void mww_reset(void);

// Most recent per-inference probability (0..1). Diagnostic only.
float mww_last_prob(void);

#ifdef __cplusplus
}
#endif
