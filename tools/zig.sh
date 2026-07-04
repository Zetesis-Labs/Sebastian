#!/usr/bin/env bash
# Wrapper estable al zig del fork Xtensa (cmake/zig.cmake lo descarga al
# compilar, con un triplete distinto por plataforma). La extensión de VS Code
# apunta aquí (zig.path) para formatear sin clavar una ruta que cambia.
for z in "$(dirname "$0")"/../firmware/build/zig-relsafe-*/zig; do
  [[ -e "$z" ]] || continue
  if "$z" version >/dev/null 2>&1; then
    exec "$z" "$@"
  fi
done

if command -v zig >/dev/null 2>&1; then
  exec zig "$@"
fi

echo "zig no ejecutable para este host — compila dentro del devcontainer o instala zig en PATH" >&2
exit 1
