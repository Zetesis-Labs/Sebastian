// microWakeWord inference engine for Sebastian.
//
// Model: sebastian.tflite  (trained with TaterTotterson/microWakeWord-Trainer-AppleSilicon)
// Input: [1, 2, 40] int8 — two consecutive 10ms filterbank feature frames.
// Output: [1, 1] uint8 — detection probability.
//
// The audio frontend converts 16kHz PCM to 40-channel log-mel filterbank
// features with a 30ms window and 10ms step. We accumulate two feature frames
// before each model Invoke(). The streaming CNN's internal state lives in the
// tensor arena and is preserved between Invoke() calls automatically.
//
// Quantization:
//   Frontend output: uint16; divide by 25.6f → float in [0, ~26] (pymicro-features convention)
//   Input quant: scale=0.10196079, zero_point=-128
//   Output quant: scale=0.00390625, zero_point=0  → float = uint8 * scale

#include "mww.h"
#include <cmath>
#include <cstring>

#include "tensorflow/lite/micro/micro_interpreter.h"
#include "tensorflow/lite/micro/micro_mutable_op_resolver.h"
#include "tensorflow/lite/micro/micro_resource_variable.h"
#include "tensorflow/lite/schema/schema_generated.h"

// Microfrontend — bundled in components/mww/microfrontend/
#include "microfrontend/frontend_util.h"
#include "microfrontend/frontend.h"

// ── Constants ────────────────────────────────────────────────────────────────

static constexpr int   kNumChannels = 40;
static constexpr int   kSampleRate  = 16000;
static constexpr int   kWindowMs    = 30;
static constexpr int   kStepMs      = 10;

// Input quantization (from model inspection: scale=0.10196079, zero_point=-128)
static constexpr float kInScale  = 0.10196079f;
static constexpr int   kInZp     = -128;
// Output quantization (scale=0.00390625 = 1/256, zero_point=0)
static constexpr float kOutScale = 0.00390625f;

// Tensor arena in internal SRAM. The model JSON says 30000, but resource
// variables (streaming state) also live here — 34KB fell 1324 bytes short.
static constexpr size_t kArenaBytes = 48 * 1024;

// ── Static state ─────────────────────────────────────────────────────────────

static uint8_t         tensor_arena[kArenaBytes] __attribute__((aligned(16)));
static FrontendState   frontend_state;
static tflite::MicroInterpreter *interpreter = nullptr;
static tflite::MicroResourceVariables *resource_vars = nullptr;

// 2-frame ring buffer (model input is [1, 2, 40])
static int8_t feat_ring[2][kNumChannels];
static int    feat_count = 0;

// Sliding window
static float  prob_win[16];
static int    prob_idx  = 0;
static int    win_size  = 4;
static float  cutoff    = 0.62f;
static float  last_prob_g = 0; // survives mww_reset for diagnostics

// ── Resolver with exactly the ops our model uses ─────────────────────────────
// Ops: CALL_ONCE, VAR_HANDLE, ASSIGN_VARIABLE, READ_VARIABLE,
//      RESHAPE, CONCATENATION, STRIDED_SLICE, CONV_2D, DEPTHWISE_CONV_2D,
//      SPLIT_V, FULLY_CONNECTED, LOGISTIC, QUANTIZE
using OpResolver = tflite::MicroMutableOpResolver<13>;
static OpResolver resolver;

static void RegisterOps() {
    resolver.AddCallOnce();
    resolver.AddVarHandle();
    resolver.AddAssignVariable();
    resolver.AddReadVariable();
    resolver.AddReshape();
    resolver.AddConcatenation();
    resolver.AddStridedSlice();
    resolver.AddConv2D();
    resolver.AddDepthwiseConv2D();
    resolver.AddSplitV();
    resolver.AddFullyConnected();
    resolver.AddLogistic();
    resolver.AddQuantize();
}

// ── Helpers ──────────────────────────────────────────────────────────────────

static inline int8_t quantize(uint16_t raw) {
    // pymicro-features (used in training) exposes the microfrontend's uint16
    // output divided by 25.6 — NOT by 256. Verified numerically against the
    // host: raw=639 → 24.96 (pymicro) vs 2.49 (raw/256, 10x too small).
    float f = raw / 25.6f;
    int   q = (int)roundf(f / kInScale) + kInZp;
    return (int8_t)(q < -128 ? -128 : (q > 127 ? 127 : q));
}

// ── Public API ───────────────────────────────────────────────────────────────

bool mww_init(const uint8_t *model_data, size_t /*model_len*/,
              float probability_cutoff, int window_size) {
    cutoff   = probability_cutoff;
    win_size = window_size < 1 ? 1 : (window_size > 16 ? 16 : window_size);

    // Audio frontend — parameters match the training configuration.
    struct FrontendConfig cfg = {};
    FrontendFillConfigWithDefaults(&cfg);
    cfg.window.size_ms               = kWindowMs; // override default (25)
    cfg.window.step_size_ms          = kStepMs;
    cfg.filterbank.num_channels      = kNumChannels;
    cfg.filterbank.lower_band_limit  = 125.0f;
    cfg.filterbank.upper_band_limit  = 7500.0f;
    cfg.pcan_gain_control.enable_pcan = 1; // matches training: enable_pcan=True
    if (!FrontendPopulateState(&cfg, &frontend_state, kSampleRate)) {
        return false;
    }

    // TFLite Micro. The streaming CNN keeps its temporal state in resource
    // variables (VAR_HANDLE ops), which require an explicit MicroResourceVariables.
    RegisterOps();
    auto *model = tflite::GetModel(model_data);
    if (model->version() != TFLITE_SCHEMA_VERSION) return false;

    static tflite::MicroAllocator *allocator =
        tflite::MicroAllocator::Create(tensor_arena, kArenaBytes);
    static tflite::MicroResourceVariables *variables =
        tflite::MicroResourceVariables::Create(allocator, 8); // model has 6 VAR_HANDLEs
    static tflite::MicroInterpreter static_interp(
        model, resolver, allocator, variables);
    interpreter = &static_interp;
    resource_vars = variables;

    if (interpreter->AllocateTensors() != kTfLiteOk) return false;

    TfLiteTensor *in = interpreter->input(0);
    if (in->dims->size != 3 ||
        in->dims->data[1] != 2 ||
        in->dims->data[2] != kNumChannels) {
        return false;
    }

    return true;
}

bool mww_feed(const int16_t *pcm_16k, int num_samples) {
    if (!interpreter) return false;

    size_t consumed = 0;
    while (consumed < (size_t)num_samples) {
        size_t num_read = 0;
        struct FrontendOutput out = FrontendProcessSamples(
            &frontend_state,
            pcm_16k + consumed,
            (size_t)num_samples - consumed,
            &num_read);
        consumed += num_read;
        if (out.size == 0) continue;

        // Quantize feature frame into the next ring slot
        int slot = feat_count % 2;
        for (int i = 0; i < kNumChannels; i++) {
            feat_ring[slot][i] = quantize(i < (int)out.size ? out.values[i] : 0);
        }
        feat_count++;

        // Streaming contract: the model consumes 2 FRESH frames per Invoke
        // (input [1,2,40], stride 20ms). Invoking on overlapping pairs would
        // advance the internal state twice as fast on duplicated data.
        if (feat_count % 2 != 0) continue;

        int8_t *inp = interpreter->input(0)->data.int8;
        memcpy(inp,                feat_ring[0], kNumChannels);
        memcpy(inp + kNumChannels, feat_ring[1], kNumChannels);

        if (interpreter->Invoke() != kTfLiteOk) continue;

        float prob = (float)interpreter->output(0)->data.uint8[0] * kOutScale;
        last_prob_g = prob;

        prob_win[prob_idx % win_size] = prob;
        prob_idx++;

        // Detection = moving average of the last win_size probabilities
        // above the cutoff (microWakeWord/ESPHome semantics).
        if (prob_idx >= win_size) {
            float sum = 0;
            for (int w = 0; w < win_size; w++) sum += prob_win[w];
            if (sum / win_size > cutoff) {
                mww_reset();
                return true;
            }
        }
    }
    return false;
}

float mww_last_prob(void) {
    return last_prob_g;
}

void mww_reset(void) {
    FrontendReset(&frontend_state);
    // Clear the streaming CNN's internal state (resource variables). Without
    // this, the context of the LAST detection survives into the next arming
    // and the model re-fires at ~99% on the first inferences — an endless
    // detect → session → close → re-detect loop.
    if (resource_vars) resource_vars->ResetAll();
    feat_count = 0;
    prob_idx   = 0;
    last_prob_g = 0;
    memset(prob_win,  0, sizeof(prob_win));
    memset(feat_ring, 0, sizeof(feat_ring));
}
