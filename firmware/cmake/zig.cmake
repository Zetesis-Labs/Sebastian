# Self-contained Zig ⇄ ESP-IDF integration.
#
# 1. Downloads the Espressif Zig fork (LLVM Xtensa backend) for the host.
# 2. Harvests the include dirs + compile defines from the `main` component's
#    ESP-IDF dependency graph (version-agnostic — no hardcoded IDF paths).
# 3. Builds main/app.zig into obj/app_zig.o and links it into the component.
#
# Expects to be include()d from main/CMakeLists.txt after idf_component_register,
# with COMPONENT_LIB defined.

# --- Host detection ----------------------------------------------------------
cmake_host_system_information(RESULT _host_os QUERY OS_NAME)
string(TOLOWER "${_host_os}" _host_os)
set(_arch "${CMAKE_HOST_SYSTEM_PROCESSOR}")
if(_arch MATCHES "^(aarch64|arm64|ARM64)$")
    set(ZIG_ARCH "aarch64")
elseif(_arch MATCHES "^(x86_64|AMD64|amd64)$")
    set(ZIG_ARCH "x86_64")
else()
    message(FATAL_ERROR "Unsupported host arch: ${_arch}")
endif()

if(_host_os MATCHES "darwin|mac|osx")
    set(ZIG_PLATFORM "macos")
    set(ARCHIVE_EXT "tar.xz")
elseif(_host_os MATCHES "linux|unix")
    set(ZIG_PLATFORM "linux-musl")
    set(ARCHIVE_EXT "tar.xz")
else()
    message(FATAL_ERROR "Unsupported host OS: ${_host_os}")
endif()

# Espressif Zig fork (kassane/zig-espressif-bootstrap, 0.16.0-xtensa)
set(ZIG_TRIPLET "${ZIG_ARCH}-${ZIG_PLATFORM}-baseline")
set(ZIG_DIR "${CMAKE_BINARY_DIR}/zig-relsafe-${ZIG_TRIPLET}")
set(ZIG_ARCHIVE "${ZIG_DIR}.${ARCHIVE_EXT}")
set(ZIG_URL "https://github.com/kassane/zig-espressif-bootstrap/releases/download/0.16.0-xtensa/zig-relsafe-${ZIG_TRIPLET}.${ARCHIVE_EXT}")

if(ZIG_ARCH STREQUAL "aarch64" AND ZIG_PLATFORM STREQUAL "macos")
    set(ZIG_HASH "7f5058c23ae822b9585ca054023676b7e07e48e4d1e265a6bd3104a55b0295ef")
elseif(ZIG_ARCH STREQUAL "x86_64" AND ZIG_PLATFORM STREQUAL "linux-musl")
    set(ZIG_HASH "9e3dcef9d6f6d552df641a12addc9e443a69b7cbdad85492ec677acd55b7de9b")
elseif(ZIG_ARCH STREQUAL "aarch64" AND ZIG_PLATFORM STREQUAL "linux-musl")
    set(ZIG_HASH "5304f43cd30dfcbdc555fde3e2b6501b4838322ba47dd71da34111e08b02eef4")
else()
    message(FATAL_ERROR "No pinned Zig hash for ${ZIG_TRIPLET}")
endif()

if(NOT EXISTS "${ZIG_DIR}/zig")
    message(STATUS "Downloading Espressif Zig fork: ${ZIG_URL}")
    file(DOWNLOAD "${ZIG_URL}" "${ZIG_ARCHIVE}"
        TLS_VERIFY ON EXPECTED_HASH SHA256=${ZIG_HASH}
        STATUS _dl SHOW_PROGRESS)
    list(GET _dl 0 _dl_code)
    if(NOT _dl_code EQUAL 0)
        message(FATAL_ERROR "Zig download failed: ${_dl}")
    endif()
    execute_process(COMMAND ${CMAKE_COMMAND} -E tar xf "${ZIG_ARCHIVE}"
        WORKING_DIRECTORY "${CMAKE_BINARY_DIR}" RESULT_VARIABLE _x)
    if(NOT _x EQUAL 0)
        message(FATAL_ERROR "Zig extraction failed")
    endif()
    file(REMOVE "${ZIG_ARCHIVE}")
endif()
set(ZIG_BIN "${ZIG_DIR}/zig")
message(STATUS "Zig: ${ZIG_BIN}")

# --- Target mapping ----------------------------------------------------------
string(TOLOWER "${CONFIG_IDF_TARGET}" _idf_model)
if(_idf_model MATCHES "esp32s3|esp32s2|^esp32$")
    set(ZIG_TARGET "xtensa-freestanding-none")
    set(ZIG_CPU "${_idf_model}")
else()
    set(ZIG_TARGET "riscv32-freestanding-none")
    set(ZIG_CPU "${_idf_model}")
endif()

# --- Toolchain libc headers (newlib) for translate-c -------------------------
# Zig has no libc for xtensa-freestanding, so translate-c needs the ESP
# toolchain's newlib + gcc headers (stdlib.h, stddef.h, …).
get_filename_component(_tc_bin "${CMAKE_C_COMPILER}" DIRECTORY)
get_filename_component(_tc_root "${_tc_bin}" DIRECTORY)
set(_tc_newlib "${_tc_root}/xtensa-esp-elf/include")
file(GLOB _tc_gcc_inc "${_tc_root}/lib/gcc/xtensa-esp-elf/*/include")

# --- Harvest include dirs + defines from the IDF component graph --------------
# Include dirs resolve at build time via generator expression (covers transitive
# REQUIRES); defines are known at configure time. Both are '|'-joined into one
# option so build.zig can split them without file I/O.
#
# Order matters: IDF includes come FIRST so the esp32s3 core-isa.h overlay
# (components/xtensa/<chip>/include) wins over the toolchain's newlib stub, which
# trails at the end only to satisfy libc headers (stdlib.h, …).
set(_inc_expr "$<JOIN:$<TARGET_PROPERTY:${COMPONENT_LIB},INCLUDE_DIRECTORIES>,|>")
set(_sys_inc_expr "${_tc_newlib}|${_tc_gcc_inc}")

idf_build_get_property(_defs COMPILE_DEFINITIONS)
string(JOIN "|" _defs_joined ${_defs})
# Xtensa little-endian + arch macros translate-c needs (newlib ieeefp.h etc.).
set(_defs_joined "${_defs_joined}|__XTENSA__|__XTENSA_EL__|__xtensa__")

# --- Build the Zig object and link it ----------------------------------------
if(CMAKE_BUILD_TYPE STREQUAL "Debug")
    set(ZIG_OPT "Debug")
else()
    set(ZIG_OPT "ReleaseSafe")
endif()

add_custom_target(zig_build
    "${ZIG_BIN}" build
        --build-file "${CMAKE_SOURCE_DIR}/build.zig"
        -Doptimize=${ZIG_OPT}
        -Dtarget=${ZIG_TARGET}
        -Dcpu=${ZIG_CPU}
        "-Dincludes=${_inc_expr}"
        "-Dsystem_includes=${_sys_inc_expr}"
        "-Ddefines=${_defs_joined}"
        --cache-dir "${CMAKE_SOURCE_DIR}/.zig-cache"
        --prefix "${CMAKE_BINARY_DIR}"
    WORKING_DIRECTORY "${CMAKE_SOURCE_DIR}"
    BYPRODUCTS "${CMAKE_BINARY_DIR}/obj/app_zig.o"
    VERBATIM)

add_dependencies(${COMPONENT_LIB} zig_build)
target_sources(${COMPONENT_LIB} PRIVATE "${CMAKE_BINARY_DIR}/obj/app_zig.o")
