#!/usr/bin/env bash
#
# Shared bash helpers for parsing VM_PRIVKEYS in scripts/init.sh and
# scripts/init_multi.sh.

# parse_vm_privkeys populates the global VM_PRIVKEY_LIST array from the
# comma-separated VM_PRIVKEYS env var. Trims whitespace on each entry and
# exits with an error on empty or all-empty input. Without this, a
# leading/trailing/double comma would slip an empty string into the list
# and `svoted keys import-hex name ""` panics; whitespace inside values
# passes through untouched and confuses the CLI into printing generic
# Usage help.
parse_vm_privkeys() {
    if [ -z "$VM_PRIVKEYS" ]; then
        echo "ERROR: VM_PRIVKEYS is not set."
        echo "  Local dev:  add VM_PRIVKEYS=<hex>[,<hex>...] to .env (see .env.example)"
        echo "  CI/deploy:  set the VM_PRIVKEYS secret in GitHub Actions"
        exit 1
    fi
    VM_PRIVKEY_LIST=()
    local raw key
    local _raw_array
    IFS=',' read -ra _raw_array <<< "$VM_PRIVKEYS"
    for raw in "${_raw_array[@]}"; do
        # Bash parameter expansion to trim leading/trailing whitespace.
        key="${raw#"${raw%%[![:space:]]*}"}"
        key="${key%"${key##*[![:space:]]}"}"
        if [ -z "$key" ]; then
            echo "ERROR: VM_PRIVKEYS contains an empty entry (leading/trailing/double comma?)."
            echo "       Fix the .env value to be a comma-separated list of non-empty 64-char hex keys."
            exit 1
        fi
        VM_PRIVKEY_LIST+=("$key")
    done
    if [ ${#VM_PRIVKEY_LIST[@]} -eq 0 ]; then
        echo "ERROR: VM_PRIVKEYS parsed to zero keys."
        exit 1
    fi
}
