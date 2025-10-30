#!/bin/sh
set -e
set -u
set -o pipefail

# Logging functions
log_info() {
  echo "[$(date -u '+%Y-%m-%d %H:%M:%S UTC')] INFO: $*"
}

log_error() {
  echo "[$(date -u '+%Y-%m-%d %H:%M:%S UTC')] ERROR: $*" >&2
}

log_warn() {
  echo "[$(date -u '+%Y-%m-%d %H:%M:%S UTC')] WARN: $*" >&2
}

# Validate required environment variables
validate_env() {
  local missing=0
  
  # Check each required variable directly (sh-compatible)
  if [ -z "${TARGET_PATH:-}" ]; then
    log_error "Required environment variable TARGET_PATH is not set"
    missing=1
  fi
  if [ -z "${FILE_MODE:-}" ]; then
    log_error "Required environment variable FILE_MODE is not set"
    missing=1
  fi
  if [ -z "${FILE_OWNER:-}" ]; then
    log_error "Required environment variable FILE_OWNER is not set"
    missing=1
  fi
  if [ -z "${FILE_GROUP:-}" ]; then
    log_error "Required environment variable FILE_GROUP is not set"
    missing=1
  fi
  
  if [ $missing -eq 1 ]; then
    log_error "Exiting due to missing required environment variables"
    exit 1
  fi
  
  # Validate source file exists
  if [ ! -f /source/content ]; then
    log_error "Source file /source/content does not exist"
    exit 1
  fi
  
  log_info "Environment validation passed"
}

# Validate file mode format (octal)
validate_mode() {
  if ! echo "${FILE_MODE}" | grep -qE '^[0-7]{3,4}$'; then
    log_error "Invalid file mode: ${FILE_MODE}. Expected octal format (e.g., 0644)"
    exit 1
  fi
}

# Inject file with retry logic
inject_file() {
  local target="/host${TARGET_PATH}"
  local target_dir
  target_dir="$(dirname "$target")"
  local max_retries=3
  local retry_delay=2
  
  log_info "Starting file injection"
  log_info "Source: /source/content"
  log_info "Target: $target"
  log_info "Mode: ${FILE_MODE}, Owner: ${FILE_OWNER}:${FILE_GROUP}"
  
  # Create target directory with retries
  local attempt=1
  while [ $attempt -le $max_retries ]; do
    if mkdir -p "$target_dir" 2>/dev/null; then
      log_info "Target directory ready: $target_dir"
      break
    else
      log_warn "Failed to create directory (attempt $attempt/$max_retries)"
      if [ $attempt -eq $max_retries ]; then
        log_error "Failed to create target directory after $max_retries attempts"
        return 1
      fi
      sleep $retry_delay
      attempt=$((attempt + 1))
    fi
  done
  
  # Backup existing file if present
  if [ -f "$target" ]; then
    local backup="${target}.backup.$(date +%s)"
    if cp "$target" "$backup" 2>/dev/null; then
      log_info "Backed up existing file to $backup"
    else
      log_warn "Could not create backup of existing file"
    fi
  fi
  
  # Copy file with retries
  attempt=1
  while [ $attempt -le $max_retries ]; do
    if cp /source/content "$target" 2>/dev/null; then
      log_info "File copied successfully"
      break
    else
      log_warn "Failed to copy file (attempt $attempt/$max_retries)"
      if [ $attempt -eq $max_retries ]; then
        log_error "Failed to copy file after $max_retries attempts"
        return 1
      fi
      sleep $retry_delay
      attempt=$((attempt + 1))
    fi
  done
  
  # Set permissions
  if ! chmod "${FILE_MODE}" "$target" 2>/dev/null; then
    log_error "Failed to set file mode ${FILE_MODE}"
    return 1
  fi
  log_info "File mode set to ${FILE_MODE}"
  
  # Set ownership
  if ! chown "${FILE_OWNER}:${FILE_GROUP}" "$target" 2>/dev/null; then
    log_error "Failed to set ownership ${FILE_OWNER}:${FILE_GROUP}"
    return 1
  fi
  log_info "File ownership set to ${FILE_OWNER}:${FILE_GROUP}"
  
  log_info "File injected successfully"
  return 0
}

# Main execution
log_info "Node File Injector (One-shot) starting..."

# Validate environment
validate_env
validate_mode

# Perform one-time file injection
if ! inject_file; then
  log_error "File injection failed"
  exit 1
fi

log_info "File injection completed successfully"
exit 0
