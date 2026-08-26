#!/usr/bin/env bash
# Storix uninstaller.
#
#   curl -fsSL https://raw.githubusercontent.com/XProject25/Storix/main/scripts/uninstall.sh | sudo bash
#
# Removes the service and the program. Your files are never touched: Storix
# only ever managed folders in place, it never moved them anywhere of its own.
#
# Developed by X Project.

set -euo pipefail

RUN_USER="${STORIX_USER:-storix}"
BIN_PATH="/usr/bin/storix"
CONFIG_DIR="/etc/storix"
DATA_DIR="/var/lib/storix"
LOG_DIR="/var/log/storix"
UNIT_FILE="/etc/systemd/system/storix.service"
PURGE=0

BOLD=""; DIM=""; GREEN=""; YELLOW=""; RESET=""
if [ -t 1 ] && command -v tput >/dev/null 2>&1 && [ "$(tput colors 2>/dev/null || echo 0)" -ge 8 ]; then
    BOLD="$(tput bold)"; DIM="$(tput dim)"; GREEN="$(tput setaf 2)"; YELLOW="$(tput setaf 3)"; RESET="$(tput sgr0)"
fi
ok()   { printf '  %s+%s %s\n' "${GREEN}" "${RESET}" "$1"; }
warn() { printf '  %s!%s %s\n' "${YELLOW}" "${RESET}" "$1"; }

while [ $# -gt 0 ]; do
    case "$1" in
        --purge) PURGE=1; shift ;;
        --help|-h)
            printf 'Storix uninstaller\n\n  --purge   also remove the database, settings and the recycle bin\n\n'
            exit 0 ;;
        *) shift ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    printf '\n  Run this with sudo.\n\n' >&2
    exit 1
fi

printf '\n  %s%sRemoving Storix%s\n\n' "${BOLD}" "${YELLOW}" "${RESET}"

if systemctl list-unit-files 2>/dev/null | grep -q '^storix\.service'; then
    systemctl stop storix 2>/dev/null || true
    systemctl disable storix 2>/dev/null || true
    ok "Service stopped and disabled"
fi
rm -f "${UNIT_FILE}"
systemctl daemon-reload 2>/dev/null || true

rm -f "${BIN_PATH}" "${BIN_PATH}.previous"
ok "Program removed"

if [ "${PURGE}" -eq 1 ]; then
    # The recycle bin lives here, so anything still inside it goes too.
    rm -rf "${DATA_DIR}" "${CONFIG_DIR}" "${LOG_DIR}"
    ok "Settings, database and recycle bin removed"
    if id -u "${RUN_USER}" >/dev/null 2>&1 && [ "${RUN_USER}" != "root" ]; then
        userdel "${RUN_USER}" 2>/dev/null || true
        ok "Service account removed"
    fi
else
    warn "Settings and database kept in ${CONFIG_DIR} and ${DATA_DIR}"
    warn "Run with --purge to remove them as well"
fi

printf '\n  %sYour files were never moved, they are exactly where they were.%s\n' "${DIM}" "${RESET}"
printf '  %sDeveloped by X Project%s\n\n' "${DIM}" "${RESET}"
