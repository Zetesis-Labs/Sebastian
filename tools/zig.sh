#!/usr/bin/env bash
# Wrapper estable al zig del fork Xtensa (cmake/zig.cmake lo descarga al
# compilar, con un triplete distinto por plataforma). La extensión de VS Code
# apunta aquí (zig.path) para formatear sin clavar una ruta que cambia.
z="$(ls "$(dirname "$0")"/../firmware/build/zig-relsafe-*/zig 2>/dev/null | head -1)"
if [[ -z "$z" ]]; then
  echo "zig no descargado — compila una vez (Firmware → Build)" >&2
  exit 1
fi
exec "$z" "$@"
