#!/usr/bin/env bash
#
# Shared genesis account helpers for auth-only vote-manager accounts and
# module-funded native-token pools.

genesis_module_address_hex() {
    if command -v sha256sum >/dev/null 2>&1; then
        printf "%s" "$1" | sha256sum | awk '{print substr($1, 1, 40)}'
    else
        printf "%s" "$1" | shasum -a 256 | awk '{print substr($1, 1, 40)}'
    fi
}

genesis_module_account_address() {
    local module_name="$1"
    local binary="${BINARY:-svoted}"
    local module_hex
    local module_addr
    module_hex=$(genesis_module_address_hex "$module_name")
    module_addr=$("$binary" debug addr "$module_hex" | awk '/Bech32 Acc:/ {print $3}')
    if [ -z "$module_addr" ]; then
        echo "ERROR: failed to derive module account address for ${module_name}" >&2
        return 1
    fi
    printf '%s\n' "$module_addr"
}

genesis_add_auth_base_account() {
    local genesis="$1"
    local addr="$2"
    jq --arg addr "$addr" '
      def account_addr: .address? // .base_account.address? // "";
      if any(.app_state.auth.accounts[]?; account_addr == $addr) then
        .
      else
        .app_state.auth.accounts += [{
          "@type": "/cosmos.auth.v1beta1.BaseAccount",
          "address": $addr,
          "pub_key": null,
          "account_number": "0",
          "sequence": "0"
        }]
      end' \
      "$genesis" > "${genesis}.tmp" && mv "${genesis}.tmp" "$genesis"
}

genesis_add_module_funding_account() {
    local genesis="$1"
    local module_name="$2"
    local denom="$3"
    local balance="$4"
    local module_addr
    local amount
    module_addr=$(genesis_module_account_address "$module_name")
    amount=${balance%"${denom}"}
    if [ -z "$amount" ] || [ "$amount" = "$balance" ]; then
        echo "ERROR: module funding balance must be formatted as <amount>${denom}, got ${balance}" >&2
        return 1
    fi
    jq --arg addr "$module_addr" \
      --arg name "$module_name" \
      --arg denom "$denom" \
      --arg amount "$amount" '
      def account_addr: .address? // .base_account.address? // "";
      def supply_amount($denom):
        ((.app_state.bank.supply // [] | map(select(.denom == $denom)) | .[0].amount) // "0");
      def balance_amount($addr; $denom):
        ((.app_state.bank.balances // []
          | map(select(.address == $addr))
          | .[0].coins // []
          | map(select(.denom == $denom))
          | .[0].amount) // "0");
      (balance_amount($addr; $denom)) as $oldAmount
      | .app_state.auth.accounts = ((.app_state.auth.accounts // [])
          | map(select(account_addr != $addr))
          + [{
            "@type": "/cosmos.auth.v1beta1.ModuleAccount",
            "base_account": {
              "address": $addr,
              "pub_key": null,
              "account_number": "0",
              "sequence": "0"
            },
            "name": $name,
            "permissions": []
          }])
      | .app_state.bank.balances = ((.app_state.bank.balances // [])
          | map(select(.address != $addr))
          + [{address: $addr, coins: [{denom: $denom, amount: $amount}]}])
      | .app_state.bank.supply = ((.app_state.bank.supply // [] | map(select(.denom != $denom)))
          + [{denom: $denom, amount: (((supply_amount($denom) | tonumber) - ($oldAmount | tonumber) + ($amount | tonumber)) | tostring)}])' \
      "$genesis" > "${genesis}.tmp" && mv "${genesis}.tmp" "$genesis"
    echo "Module funding account ${module_name}: $module_addr (balance: $balance)"
}
