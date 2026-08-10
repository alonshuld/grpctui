# This Dockerfile does not build grpctui — it packages a binary GoReleaser has
# already cross-compiled, which is why there is no `go build` here and why
# `docker build .` on its own fails: the `grpctui` it copies exists only in the
# context GoReleaser assembles. `make snapshot` is how you exercise it locally.
FROM alpine:3.22

# ca-certificates: the binary is static (CGO_ENABLED=0), so nothing else drags
# them in, and without them every TLS target fails to dial with an opaque
# certificate error. vim, nano and less are here because this image is meant to
# be opened and poked around in — a scratch or distroless base has no shell at
# all, which makes `docker exec` and `--entrypoint sh` both dead ends.
RUN apk add --no-cache ca-certificates vim nano less

# GoReleaser stages the binaries it has already cross-compiled into a context
# laid out by platform — linux/amd64/grpctui, linux/arm64/grpctui — and buildx
# fills in TARGETPLATFORM per platform it is building. Copying a bare `grpctui`
# finds nothing: there is no such path in that context.
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/grpctui /usr/local/bin/grpctui

# Pinned rather than left to $HOME, so the paths hold whatever uid the container
# runs as. `--user $(id -u)` is the documented way to keep a bind mount from
# filling up with root-owned history files, and it leaves HOME unset — grpctui
# would then resolve ~/.config to /.config and write its state somewhere nobody
# mounted. XDG_CONFIG_HOME and XDG_STATE_HOME are honoured by every path in
# internal/config, internal/requests and internal/logging.
ENV XDG_CONFIG_HOME=/config \
    XDG_STATE_HOME=/state \
    TERM=xterm-256color

# World-writable because the uid is the caller's choice, and an unmounted run
# still has to be able to save a collection. The leaf directories need it as
# much as their parents do — grpctui writes history.yaml and collections/ into
# them, so leaving those root-owned 755 is what actually breaks `--user`.
RUN mkdir -p /config/grpctui /state/grpctui \
 && chmod 1777 /config /state /config/grpctui /state/grpctui

VOLUME ["/config", "/state"]

ENTRYPOINT ["/usr/local/bin/grpctui"]
