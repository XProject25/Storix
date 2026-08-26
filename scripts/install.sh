#!/usr/bin/env bash
# Storix installer.
#
#   curl -fsSL https://raw.githubusercontent.com/XProject25/Storix/main/scripts/install.sh | sudo bash
#
# It detects the distribution and architecture, installs the binary, creates a
# service account, writes the configuration, registers the systemd service,
# opens the firewall port and prints the link to the first run wizard.
#
# Developed by X Project.

set -euo pipefail

REPO="${STORIX_REPO:-XProject25/Storix}"
VERSION="${STORIX_VERSION:-latest}"
PORT="${STORIX_PORT:-8686}"
RUN_USER="${STORIX_USER:-storix}"
BIN_PATH="/usr/bin/storix"
CONFIG_DIR="/etc/storix"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
DATA_DIR="/var/lib/storix"
LOG_DIR="/var/log/storix"
UNIT_FILE="/etc/systemd/system/storix.service"
TMP_DIR=""

BOLD=""; DIM=""; RED=""; GREEN=""; YELLOW=""; CYAN=""; RESET=""
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    BOLD="$(tput bold)"; DIM="$(tput dim)"; RED="$(tput setaf 1)"; GREEN="$(tput setaf 2)"
    YELLOW="$(tput setaf 3)"; CYAN="$(tput setaf 6)"; RESET="$(tput sgr0)"
fi

step()  { printf '  %s>%s %s\n' "${CYAN}" "${RESET}" "$1"; }
ok()    { printf '  %s+%s %s\n' "${GREEN}" "${RESET}" "$1"; }
warn()  { printf '  %s!%s %s\n' "${YELLOW}" "${RESET}" "$1"; }
fail()  { printf '\n  %s%sInstallation stopped%s\n    %s\n\n' "${RED}" "${BOLD}" "${RESET}" "$1" >&2; exit 1; }

cleanup() { [ -n "${TMP_DIR}" ] && [ -d "${TMP_DIR}" ] && rm -rf "${TMP_DIR}"; }
trap cleanup EXIT

banner() {
    printf '\n'
    printf '  %s%sStorix%s  modern web file manager for servers\n' "${BOLD}" "${CYAN}" "${RESET}"
    printf '  %sDeveloped by X Project%s\n\n' "${DIM}" "${RESET}"
}

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "Run this with sudo:  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sudo bash"
    fi
}

detect_os() {
    if [ ! -r /etc/os-release ]; then
        fail "This system does not look like a supported Linux distribution."
    fi
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-unknown}"
    OS_NAME="${PRETTY_NAME:-${OS_ID}}"
    case "${OS_ID}" in
        ubuntu|debian|linuxmint|pop|raspbian|elementary|zorin)
            ok "Detected ${OS_NAME}"
            ;;
        fedora|rhel|centos|rocky|almalinux|opensuse*|arch|manjaro)
            ok "Detected ${OS_NAME}"
            warn "Storix is tested on Ubuntu and Debian. It should still work here."
            ;;
        *)
            warn "Unrecognised distribution ${OS_NAME}, continuing anyway"
            ;;
    esac
    if ! command -v systemctl >/dev/null 2>&1; then
        fail "systemd is required and was not found on this system."
    fi
}

detect_arch() {
    local machine
    machine="$(uname -m)"
    case "${machine}" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        armv7l|armhf)   ARCH="arm" ;;
        *) fail "Unsupported processor architecture: ${machine}" ;;
    esac
    ok "Detected ${machine} (${ARCH})"
}

need_tools() {
    local missing=""
    for tool in curl tar; do
        command -v "${tool}" >/dev/null 2>&1 || missing="${missing} ${tool}"
    done
    if [ -n "${missing}" ]; then
        step "Installing required tools:${missing}"
        if command -v apt-get >/dev/null 2>&1; then
            DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 || true
            # shellcheck disable=SC2086
            DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ${missing} >/dev/null 2>&1 \
                || fail "Could not install:${missing}"
        elif command -v dnf >/dev/null 2>&1; then
            # shellcheck disable=SC2086
            dnf install -y -q ${missing} >/dev/null 2>&1 || fail "Could not install:${missing}"
        else
            fail "Please install these first:${missing}"
        fi
    fi
}

resolve_release() {
    local api url
    if [ "${VERSION}" = "latest" ]; then
        api="https://api.github.com/repos/${REPO}/releases/latest"
    else
        api="https://api.github.com/repos/${REPO}/releases/tags/${VERSION#v}"
    fi
    local body
    body="$(curl -fsSL -H 'Accept: application/vnd.github+json' "${api}" 2>/dev/null || true)"
    if [ -z "${body}" ]; then
        fail "Could not reach the release feed for ${REPO}. Check the server internet connection."
    fi
    RELEASE_TAG="$(printf '%s' "${body}" | grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | cut -d'"' -f4)"
    if [ -z "${RELEASE_TAG}" ]; then
        printf '\n'
        warn "No published release was found for ${REPO}."
        warn "Build from source instead:"
        printf '\n      git clone https://github.com/%s.git\n      cd Storix && make install\n\n' "${REPO}"
        exit 1
    fi
    RELEASE_VERSION="${RELEASE_TAG#v}"
    ASSET="storix_${RELEASE_VERSION}_linux_${ARCH}.tar.gz"
    url="$(printf '%s' "${body}" | grep -o "https://[^\"]*${ASSET}" | head -1)"
    if [ -z "${url}" ]; then
        fail "Release ${RELEASE_TAG} has no build for linux/${ARCH}."
    fi
    ASSET_URL="${url}"
    SUMS_URL="$(printf '%s' "${body}" | grep -o 'https://[^"]*checksums\.txt' | head -1)"
}

download_and_verify() {
    TMP_DIR="$(mktemp -d)"
    step "Downloading Storix ${RELEASE_VERSION} for linux/${ARCH}"
    curl -fsSL --retry 3 --retry-delay 2 -o "${TMP_DIR}/${ASSET}" "${ASSET_URL}" \
        || fail "Download failed. Check the server internet connection."

    if [ -n "${SUMS_URL:-}" ] && command -v sha256sum >/dev/null 2>&1; then
        curl -fsSL -o "${TMP_DIR}/checksums.txt" "${SUMS_URL}" || true
        if [ -s "${TMP_DIR}/checksums.txt" ]; then
            local want got
            want="$(grep " \*\?${ASSET}\$" "${TMP_DIR}/checksums.txt" | awk '{print $1}' | head -1)"
            got="$(sha256sum "${TMP_DIR}/${ASSET}" | awk '{print $1}')"
            if [ -n "${want}" ] && [ "${want}" != "${got}" ]; then
                fail "The download does not match its published checksum. Nothing was installed."
            fi
            [ -n "${want}" ] && ok "Checksum verified"
        fi
    fi

    tar -xzf "${TMP_DIR}/${ASSET}" -C "${TMP_DIR}" || fail "The downloaded archive could not be opened."
    if [ ! -f "${TMP_DIR}/storix" ]; then
        # Some archives keep the binary one level down.
        local found
        found="$(find "${TMP_DIR}" -maxdepth 3 -type f -name storix | head -1)"
        [ -n "${found}" ] || fail "The archive does not contain a storix binary."
        mv "${found}" "${TMP_DIR}/storix"
    fi
    chmod 0755 "${TMP_DIR}/storix"
}

create_account() {
    if [ "${RUN_USER}" = "root" ]; then
        warn "Storix will run as root, which gives it access to every file on this server"
        return
    fi
    if id -u "${RUN_USER}" >/dev/null 2>&1; then
        ok "Service account ${RUN_USER} already exists"
    else
        step "Creating the service account ${RUN_USER}"
        if command -v useradd >/dev/null 2>&1; then
            useradd --system --no-create-home --home-dir "${DATA_DIR}" \
                --shell /usr/sbin/nologin --comment "Storix service" "${RUN_USER}" \
                || fail "Could not create the ${RUN_USER} account."
        else
            adduser --system --no-create-home --home "${DATA_DIR}" --disabled-login "${RUN_USER}" \
                || fail "Could not create the ${RUN_USER} account."
        fi
        ok "Service account created"
    fi
    # Joining www-data lets Storix manage a typical web root out of the box.
    if getent group www-data >/dev/null 2>&1; then
        usermod -aG www-data "${RUN_USER}" 2>/dev/null || true
    fi
}

install_files() {
    step "Installing ${BIN_PATH}"
    install -o root -g root -m 0755 "${TMP_DIR}/storix" "${BIN_PATH}"

    mkdir -p "${CONFIG_DIR}" "${DATA_DIR}" "${LOG_DIR}"
    chmod 0750 "${CONFIG_DIR}" "${DATA_DIR}" "${LOG_DIR}"
    if [ "${RUN_USER}" != "root" ]; then
        chown root:"${RUN_USER}" "${CONFIG_DIR}"
        chown -R "${RUN_USER}":"${RUN_USER}" "${DATA_DIR}" "${LOG_DIR}"
    fi

    if [ -f "${CONFIG_FILE}" ]; then
        ok "Keeping the existing configuration"
    else
        step "Writing ${CONFIG_FILE}"
        cat > "${CONFIG_FILE}" <<EOF
# Storix configuration
# Developed by X Project - https://github.com/${REPO}
server:
  host: 0.0.0.0
  port: ${PORT}
  domain: ""
  tls:
    mode: "off"
    email: ""
    redirect_http: true

storage:
  data_dir: ${DATA_DIR}

security:
  session_ttl: 168h
  allow_advanced: true
  login_rate_burst: 8
  login_rate_window: 5m
  login_lockout: 15m

limits:
  max_upload_size: 0
  upload_chunk_size: 16777216
  trash_retention: 720h

log:
  level: info
  file: ${LOG_DIR}/storix.log
  format: text
  access_log: false
EOF
        chmod 0640 "${CONFIG_FILE}"
        [ "${RUN_USER}" != "root" ] && chown root:"${RUN_USER}" "${CONFIG_FILE}"
    fi
}

install_service() {
    step "Registering the storix service"
    cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=Storix file manager
Documentation=https://github.com/${REPO}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${RUN_USER}
Group=${RUN_USER}
ExecStart=${BIN_PATH} serve --config ${CONFIG_FILE}
Restart=always
RestartSec=3
KillSignal=SIGTERM
TimeoutStopSec=30
WorkingDirectory=${DATA_DIR}

# Large transfers need plenty of file descriptors.
LimitNOFILE=65535

# Storix binds low ports only when automatic HTTPS is switched on.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

# Hardening. The service still reaches the folders an administrator adds.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
ReadWritePaths=${DATA_DIR} ${LOG_DIR}

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable storix >/dev/null 2>&1 || true
}

open_firewall() {
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qi '^Status: active'; then
        step "Opening port ${PORT} in the firewall"
        ufw allow "${PORT}/tcp" >/dev/null 2>&1 || warn "Could not add the firewall rule automatically"
        ok "Firewall rule added"
    elif command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        step "Opening port ${PORT} in the firewall"
        firewall-cmd --permanent --add-port="${PORT}/tcp" >/dev/null 2>&1 || true
        firewall-cmd --reload >/dev/null 2>&1 || true
        ok "Firewall rule added"
    fi
}

start_service() {
    step "Starting Storix"
    systemctl restart storix
    local waited=0
    while [ "${waited}" -lt 25 ]; do
        if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/api/v1/system/status" >/dev/null 2>&1; then
            ok "Storix is running"
            return 0
        fi
        sleep 1
        waited=$((waited + 1))
    done
    warn "Storix did not answer within 25 seconds"
    printf '\n%s\n' "$(journalctl -u storix -n 25 --no-pager 2>/dev/null || true)"
    fail "Check the logs with: journalctl -u storix -f"
}

primary_address() {
    local addr
    addr="$(curl -fsS --max-time 4 https://api.ipify.org 2>/dev/null || true)"
    if [ -z "${addr}" ]; then
        addr="$(hostname -I 2>/dev/null | awk '{print $1}')"
    fi
    [ -z "${addr}" ] && addr="127.0.0.1"
    printf '%s' "${addr}"
}

finish() {
    local address token url
    address="$(primary_address)"
    token="$(cat "${DATA_DIR}/setup-token" 2>/dev/null | tr -d '\n' || true)"
    url="http://${address}:${PORT}"
    [ -n "${token}" ] && url="${url}/setup?token=${token}"

    printf '\n'
    printf '  %s%sStorix %s is installed%s\n\n' "${GREEN}" "${BOLD}" "${RELEASE_VERSION}" "${RESET}"
    printf '  Open this link to finish the setup:\n\n'
    printf '    %s%s%s\n\n' "${BOLD}" "${url}" "${RESET}"
    if [ -z "${token}" ]; then
        printf '  %sPrint the setup link again with:%s  sudo storix setup-token\n\n' "${DIM}" "${RESET}"
    fi
    printf '  %sService%s      systemctl status storix\n' "${DIM}" "${RESET}"
    printf '  %sLogs%s         journalctl -u storix -f\n' "${DIM}" "${RESET}"
    printf '  %sHealth%s       sudo storix doctor\n' "${DIM}" "${RESET}"
    printf '  %sConfig%s       %s\n' "${DIM}" "${RESET}" "${CONFIG_FILE}"
    if [ "${RUN_USER}" != "root" ]; then
        printf '\n  %sStorix runs as the %s account, so it only reaches folders that%s\n' "${DIM}" "${RUN_USER}" "${RESET}"
        printf '  %saccount can read. If a folder you add shows as empty, grant access with:%s\n' "${DIM}" "${RESET}"
        printf '    setfacl -R -m u:%s:rwx /path/to/folder\n' "${RUN_USER}"
    fi
    printf '\n  %sDeveloped by X Project%s\n\n' "${DIM}" "${RESET}"
}

main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --port)    PORT="$2"; shift 2 ;;
            --user)    RUN_USER="$2"; shift 2 ;;
            --version) VERSION="$2"; shift 2 ;;
            --yes|-y)  shift ;;  # accepted for scripted installs, nothing is ever prompted
            --help|-h)
                printf 'Storix installer\n\n  --port N       listen port, default 8686\n  --user NAME    service account, default storix, use root for full access\n  --version TAG  install a specific release\n  --yes          do not ask anything\n\n'
                exit 0 ;;
            *) shift ;;
        esac
    done

    banner
    require_root
    detect_os
    detect_arch
    need_tools
    resolve_release
    download_and_verify
    create_account
    install_files
    install_service
    open_firewall
    start_service
    finish
}

main "$@"
