FROM espressif/idf:release-v5.4

# uv: agente + token server + scripts (gestiona sus propios Python)
RUN curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh

# El entrypoint de la imagen ya exporta el entorno IDF, pero los shells del
# devcontainer (VS Code / docker exec) no pasan por él: bashrc cubre los
# interactivos y profile.d los de login (docker compose exec bash -lc).
RUN echo '. "$IDF_PATH/export.sh" > /dev/null 2>&1 || true' >> /etc/bash.bashrc \
    && echo '. "$IDF_PATH/export.sh" > /dev/null 2>&1 || true' > /etc/profile.d/esp-idf.sh
