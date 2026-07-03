#!/usr/bin/env bash
set -euo pipefail

# MiBee NVR — One-click installer for Linux
# Usage: curl -fsSL https://raw.githubusercontent.com/Mi-Bee-Studio/MiBeeNvr/main/install.sh | bash
#        or: ./install.sh --version v0.2.0
#        or: ./install.sh --uninstall

REPO="Mi-Bee-Studio/MiBeeNvr"
DATA_DIR="/var/lib/mibee-nvr"
BIN_PATH="/usr/local/bin/mibee-nvr"
SERVICE_NAME="mibee-nvr"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
SERVICE_URL="https://raw.githubusercontent.com/${REPO}/main/deploy/${SERVICE_NAME}.service"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC} $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; }

detect_downloader() {
    if command -v curl &>/dev/null; then
        echo "curl"
    elif command -v wget &>/dev/null; then
        echo "wget"
    else
        error "Neither curl nor wget found. Please install one and retry."
        exit 1
    fi
}

download() {
    local url="$1" output="$2"
    case "${DL:-}" in
        curl) curl -fsSL -o "$output" "$url" ;;
        wget) wget -q -O "$output" "$url" ;;
    esac
}

download_stdout() {
    local url="$1"
    case "${DL:-}" in
        curl) curl -fsSL "$url" ;;
        wget) wget -qO- "$url" ;;
    esac
}

detect_arch() {
    local machine
    machine="$(uname -m)"
    case "$machine" in
        aarch64|arm64)  echo "arm64" ;;
        x86_64|amd64)   echo "amd64" ;;
        armv7l|armhf)   echo "arm64" ;;
        *)
            error "Unsupported architecture: $machine"
            exit 1
            ;;
    esac
}

get_latest_version() {
    local json
    json="$(download_stdout "https://api.github.com/repos/${REPO}/releases/latest")"
    echo "$json" | grep '"tag_name"' | head -1 | sed -E 's/.*"tag_name":\s*"([^"]+)".*/\1/'
}

get_download_url() {
    local version="$1" arch="$2"
    echo "https://github.com/${REPO}/releases/download/${version}/mibee-nvr-${arch}"
}

check_root() {
    if [[ "$(id -u)" -ne 0 ]]; then
        error "This script must be run as root. Use: sudo $0"
        exit 1
    fi
}

create_user() {
    if id "nvr" &>/dev/null; then
        info "User 'nvr' already exists, skipping."
    else
        useradd -r -s /bin/false -d "${DATA_DIR}" nvr
        info "Created system user 'nvr'."
    fi
}

setup_data_dir() {
    if [[ ! -d "${DATA_DIR}" ]]; then
        mkdir -p "${DATA_DIR}"
        info "Created data directory ${DATA_DIR}."
    fi
    chown -R nvr:nvr "${DATA_DIR}"
}

install_binary() {
    local arch="$1" version="$2"
    local url
    url="$(get_download_url "$version" "$arch")"
    local tmp
    tmp="$(mktemp)"

    info "Downloading MiBee NVR ${version} for ${arch}..."
    if ! download "$url" "$tmp"; then
        rm -f "$tmp"
        error "Failed to download binary from ${url}"
        error "Check that version ${version} exists and supports ${arch}."
        exit 1
    fi

    chmod +x "$tmp"
    mv "$tmp" "${BIN_PATH}"
    info "Installed binary to ${BIN_PATH}."
}

init_config() {
    if [[ -f "${DATA_DIR}/mibee-nvr.yaml" ]]; then
        info "Config file already exists at ${DATA_DIR}/mibee-nvr.yaml, skipping init."
        return 0
    fi

    info "No config file found. Running init..."
    echo ""
    read -rp "Enter admin password for Web UI: " -s INSTALL_PASSWORD
    echo ""
    if [[ -z "$INSTALL_PASSWORD" ]]; then
        error "Password cannot be empty."
        exit 1
    fi

    sudo -u nvr -- "${BIN_PATH}" init \
        --password "$INSTALL_PASSWORD" \
        --data-dir "${DATA_DIR}" \
        --config "${DATA_DIR}/mibee-nvr.yaml" \
        --listen ":9090"

    info "Config initialized at ${DATA_DIR}/mibee-nvr.yaml."
}

install_service() {
    local tmp
    tmp="$(mktemp)"

    info "Downloading systemd service file..."
    if ! download "$SERVICE_URL" "$tmp"; then
        rm -f "$tmp"
        error "Failed to download service file."
        exit 1
    fi

    sed -i \
        -e "s|ExecStart=.*|ExecStart=${BIN_PATH} -config ${DATA_DIR}/mibee-nvr.yaml|" \
        -e "s|WorkingDirectory=.*|WorkingDirectory=${DATA_DIR}|" \
        "$tmp"

    cp "$tmp" "${SERVICE_FILE}"
    rm -f "$tmp"
    chmod 644 "${SERVICE_FILE}"

    info "Installed systemd service to ${SERVICE_FILE}."
}

install_optional_ffmpeg() {
    # FFmpeg is OPTIONAL — only needed for H.265↔H.264 transcoding (storage
    # optimization + live relay transcode). All other features work without it.
    if command -v ffmpeg &>/dev/null; then
        info "FFmpeg already installed (transcoding available)."
        return 0
    fi

    local pkg_mgr=""
    if command -v apt-get &>/dev/null; then
        pkg_mgr="apt-get"
    elif command -v dnf &>/dev/null; then
        pkg_mgr="dnf"
    elif command -v yum &>/dev/null; then
        pkg_mgr="yum"
    elif command -v apk &>/dev/null; then
        pkg_mgr="apk"
    fi

    if [[ -z "$pkg_mgr" ]]; then
        warn "FFmpeg not found and no supported package manager detected."
        warn "Transcoding will be disabled. Install ffmpeg manually to enable it,"
        warn "or use the in-app downloader (Settings → Transcoding)."
        return 0
    fi

    info "Attempting to install FFmpeg (optional, for transcoding) via ${pkg_mgr}..."
    case "$pkg_mgr" in
        apt-get) DEBIAN_FRONTEND=noninteractive apt-get update -qq && apt-get install -y -qq ffmpeg ;;
        dnf|yum) $pkg_mgr install -y -q ffmpeg ;;
        apk) apk add --no-cache ffmpeg ;;
    esac

    if command -v ffmpeg &>/dev/null; then
        info "FFmpeg installed successfully — transcoding enabled."
    else
        warn "FFmpeg installation failed. Transcoding will be disabled."
        warn "All other features work normally. Install ffmpeg manually to enable transcoding."
    fi
}

enable_service() {
    systemctl daemon-reload
    systemctl enable "${SERVICE_NAME}" &>/dev/null
    systemctl restart "${SERVICE_NAME}"
    info "Service enabled and started."
}

wait_for_ready() {
    local attempts=0
    while [[ $attempts -lt 15 ]]; do
        if systemctl is-active --quiet "${SERVICE_NAME}"; then
            return 0
        fi
        attempts=$((attempts + 1))
        sleep 1
    done
    warn "Service may not have started correctly. Check: systemctl status ${SERVICE_NAME}"
    return 1
}

print_success() {
    local listen
    listen=$(grep -oP '(?<=Listen:)\s*:\d+' "${DATA_DIR}/mibee-nvr.yaml" 2>/dev/null | tr -d '[:space:]')
    listen="${listen:-:9090}"
    local port="${listen#:}"

    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}  MiBee NVR installed successfully!${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo ""
    echo "  Web UI:   http://$(hostname -I 2>/dev/null | awk '{print $1}' || echo localhost):${port}"
    echo "  Config:   ${DATA_DIR}/mibee-nvr.yaml"
    echo "  Data:     ${DATA_DIR}"
    echo "  Binary:   ${BIN_PATH}"
    echo "  Service:  systemctl {start|stop|restart|status} ${SERVICE_NAME}"
    echo ""
    echo "  Next: Edit config to add cameras:"
    echo "    sudo nano ${DATA_DIR}/mibee-nvr.yaml"
    echo "    sudo systemctl restart ${SERVICE_NAME}"
    echo ""
    echo "  Note: Transcoding (H.265↔H.264) is optional and needs FFmpeg."
    echo "        All other features work without it."
    echo ""
}

do_uninstall() {
    check_root

    info "Stopping service..."
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true

    if [[ -f "${SERVICE_FILE}" ]]; then
        rm -f "${SERVICE_FILE}"
        systemctl daemon-reload
        info "Removed service file."
    fi

    if [[ -f "${BIN_PATH}" ]]; then
        rm -f "${BIN_PATH}"
        info "Removed binary."
    fi

    if id "nvr" &>/dev/null; then
        userdel nvr 2>/dev/null || true
        info "Removed user 'nvr'."
    fi

    echo ""
    if [[ -d "${DATA_DIR}" ]]; then
        warn "Data directory ${DATA_DIR} was NOT removed (preserves recordings)."
        warn "To remove: sudo rm -rf ${DATA_DIR}"
    fi

    echo ""
    info "MiBee NVR uninstalled."
}

do_install() {
    local version=""
    local arch=""

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --version)
                shift
                if [[ $# -eq 0 ]]; then
                    error "--version requires an argument (e.g. v0.2.0)"
                    exit 1
                fi
                version="$1"
                ;;
            *)
                error "Unknown option: $1"
                echo "Usage: $0 [--version <tag>] [--uninstall]"
                exit 1
                ;;
        esac
        shift
    done

    check_root

    DL="$(detect_downloader)"
    arch="$(detect_arch)"

    if [[ -z "$version" ]]; then
        info "Detecting latest version..."
        version="$(get_latest_version)"
        if [[ -z "$version" ]]; then
            error "Failed to detect latest version."
            exit 1
        fi
    fi

    info "Installing MiBee NVR ${version} (${arch})..."

    create_user
    setup_data_dir
    install_binary "$arch" "$version"
    init_config
    install_service
    install_optional_ffmpeg
    enable_service
    wait_for_ready
    print_success
}

if [[ "${1:-}" == "--uninstall" ]]; then
    do_uninstall
else
    do_install "$@"
fi
