set(MICROFRONTEND_SRCS
    microfrontend/fft.c
    microfrontend/fft_util.c
    microfrontend/filterbank.c
    microfrontend/filterbank_util.c
    microfrontend/frontend.c
    microfrontend/frontend_util.c
    microfrontend/kiss_fft.c
    microfrontend/kiss_fftr.c
    microfrontend/log_lut.c
    microfrontend/log_scale.c
    microfrontend/log_scale_util.c
    microfrontend/noise_reduction.c
    microfrontend/noise_reduction_util.c
    microfrontend/pcan_gain_control.c
    microfrontend/pcan_gain_control_util.c
    microfrontend/window.c
    microfrontend/window_util.c
)

idf_component_register(
    SRCS "mww.cpp" ${MICROFRONTEND_SRCS}
    INCLUDE_DIRS "." "microfrontend"
    REQUIRES esp-tflite-micro
)

target_compile_options(${COMPONENT_LIB} PRIVATE
    -fno-exceptions
    -fno-rtti
    -Wno-unused-parameter
    -Wno-sign-compare
)
