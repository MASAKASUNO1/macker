#!/usr/bin/env bash
# Set up passwordless (key-based) ssh to a macker node, run FROM the client.
#
# What it does (idempotent, safe to re-run):
#   1. ensure a local ssh key exists (generate ed25519 if missing)
#   2. if login is already passwordless, stop early
#   3. install the public key on the remote (ssh-copy-id; ssh prompts for the
#      password ONCE — this script never reads or stores it)
#   4. add an idempotent ~/.ssh/config block so `macker <node>` and plain ssh
#      use the right User / IdentityFile from now on
#   5. verify a passwordless, non-interactive login works
#
# It does NOT touch the remote sshd config (PasswordAuthentication stays on).
#
# Usage:
#   bash ssh-setup.sh --host HOST [--user USER] [--node NAME]
#                     [--key PATH] [--port N] [--ask-passphrase]
#                     [--no-config] [--test-only]
#
# Flags:
#   --host HOST        ssh target: MagicDNS name (node.tailnet.ts.net) or IP
#   --node NAME        macker logical node name; with Tailscale, resolves --host
#   --user USER        remote account name (default: local $USER)
#   --key PATH         private key path (default: ~/.ssh/id_ed25519)
#   --port N           ssh port (default: 22)
#   --ask-passphrase   prompt for a key passphrase when generating (default: none)
#   --no-config        do not write a ~/.ssh/config block
#   --test-only        only check whether passwordless login already works
set -euo pipefail

HOST=""
NODE=""
USER_REMOTE=""
KEY="$HOME/.ssh/id_ed25519"
PORT="22"
ASK_PASSPHRASE=0
DO_CONFIG=1
TEST_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --host)           HOST="${2:-}"; shift 2 ;;
    --node)           NODE="${2:-}"; shift 2 ;;
    --user)           USER_REMOTE="${2:-}"; shift 2 ;;
    --key)            KEY="${2:-}"; shift 2 ;;
    --port)           PORT="${2:-}"; shift 2 ;;
    --ask-passphrase) ASK_PASSPHRASE=1; shift ;;
    --no-config)      DO_CONFIG=0; shift ;;
    --test-only)      TEST_ONLY=1; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

USER_REMOTE="${USER_REMOTE:-$USER}"

# 1. Resolve --host from --node via Tailscale if needed --------------------
# Prefer the MagicDNS name (matches what `macker <node>` dials). Needs the
# --json output, so parse it with python3 (ships with macOS CommandLineTools).
resolve_host_from_node() {
  local ts="" name="$1"
  ts="$(command -v tailscale || true)"
  [ -z "$ts" ] && [ -x /Applications/Tailscale.app/Contents/MacOS/Tailscale ] \
    && ts=/Applications/Tailscale.app/Contents/MacOS/Tailscale
  [ -z "$ts" ] && return 1
  command -v python3 >/dev/null 2>&1 || return 1
  "$ts" status --json 2>/dev/null | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(1)
want=sys.argv[1].lower()
peers=list(d.get("Peer",{}).values())
if d.get("Self"): peers.append(d["Self"])
for p in peers:
    host=(p.get("HostName") or "").lower()
    dns=(p.get("DNSName") or "").rstrip(".")
    short=dns.split(".")[0].lower() if dns else ""
    if want in (host, short):
        print(dns or (p.get("TailscaleIPs") or [""])[0]); sys.exit(0)
sys.exit(1)
' "$name" 2>/dev/null
}

if [ -z "$HOST" ] && [ -n "$NODE" ]; then
  info "Tailscale から '$NODE' のアドレスを解決"
  if HOST="$(resolve_host_from_node "$NODE")" && [ -n "$HOST" ]; then
    ok "解決: $NODE -> $HOST"
  else
    die "ノード '$NODE' を Tailscale から解決できません。--host で MagicDNS 名か IP を直接指定してください。"
  fi
fi
[ -n "$HOST" ] || die "--host(または Tailscale 解決可能な --node)が必要です"

TARGET="$USER_REMOTE@$HOST"
SSH_BASE=(-p "$PORT" -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10)

# Helper: does a key-only, non-interactive login already work?
passwordless_works() {
  ssh "${SSH_BASE[@]}" -o BatchMode=yes -o PreferredAuthentications=publickey \
    -o PubkeyAuthentication=yes "$TARGET" true 2>/dev/null
}

# --test-only: just report and exit -----------------------------------------
if [ "$TEST_ONLY" = 1 ]; then
  info "パスワードレスログインを確認: $TARGET (port $PORT)"
  if passwordless_works; then ok "鍵でログインできます(パスワード不要)"; exit 0
  else die "まだパスワードが必要です。--test-only を外して鍵を設定してください。"; fi
fi

# 2. Ensure a local key ------------------------------------------------------
if [ -f "$KEY" ]; then
  ok "ローカル鍵: $KEY (既存)"
else
  info "ローカル鍵が無いので生成: $KEY"
  mkdir -p "$(dirname "$KEY")"; chmod 700 "$(dirname "$KEY")"
  if [ "$ASK_PASSPHRASE" = 1 ]; then
    ssh-keygen -t ed25519 -f "$KEY" -C "macker-$(hostname -s 2>/dev/null || echo host)"
  else
    # No passphrase: the whole point is a hands-free 2nd login. For higher
    # security re-run with --ask-passphrase and load it into ssh-agent.
    ssh-keygen -t ed25519 -N "" -f "$KEY" -C "macker-$(hostname -s 2>/dev/null || echo host)" >/dev/null
    warn "パスフレーズ無しの鍵を作成しました(自動ログイン優先)。"
    warn "より安全にするなら --ask-passphrase で作り直し、ssh-agent に登録してください。"
  fi
  ok "鍵を生成: $KEY / $KEY.pub"
fi
[ -f "$KEY.pub" ] || die "公開鍵が見つかりません: $KEY.pub"

# On macOS, register the key with ssh-agent + keychain so a passphrase-protected
# key still gives passwordless attach. No-op for passphrase-less keys.
if [ "$(uname -s)" = "Darwin" ]; then
  ssh-add --apple-use-keychain "$KEY" >/dev/null 2>&1 || true
fi

# 3. Already done? -----------------------------------------------------------
info "現状を確認: $TARGET で鍵ログインできるか"
if passwordless_works; then
  ok "既にパスワードレスでログインできます。公開鍵の配布はスキップ。"
else
  # 4. Distribute the public key. ssh prompts for the password ONCE here; this
  #    script never sees it. Prefer ssh-copy-id (dedupes); fall back to an
  #    idempotent append if it is missing.
  info "公開鍵をリモートへ配布(ここでリモートのパスワードを一度だけ聞かれます)"
  if command -v ssh-copy-id >/dev/null 2>&1; then
    ssh-copy-id -i "$KEY.pub" -o StrictHostKeyChecking=accept-new -p "$PORT" "$TARGET" \
      || die "ssh-copy-id に失敗。ユーザ名/ホスト/パスワードとリモートログイン(sshd)を確認してください。"
  else
    warn "ssh-copy-id が無いのでフォールバックで配布"
    PUBKEY="$(cat "$KEY.pub")"
    ssh "${SSH_BASE[@]}" "$TARGET" \
      "umask 077; mkdir -p ~/.ssh; touch ~/.ssh/authorized_keys; \
       grep -qxF \"$PUBKEY\" ~/.ssh/authorized_keys || printf '%s\n' \"$PUBKEY\" >> ~/.ssh/authorized_keys" \
      || die "公開鍵の配布に失敗しました。"
  fi
  ok "公開鍵を配布しました。"
fi

# 5. ~/.ssh/config block -----------------------------------------------------
# So plain `ssh <host>` and macker's `ssh <node> tmux attach` pick the right
# remote user + key without asking. Keyed by a marker comment for idempotency.
if [ "$DO_CONFIG" = 1 ]; then
  CFG="$HOME/.ssh/config"
  MARK="# macker: passwordless attach to $HOST"
  mkdir -p "$HOME/.ssh"; chmod 700 "$HOME/.ssh"
  touch "$CFG"; chmod 600 "$CFG"
  if grep -qF "$MARK" "$CFG" 2>/dev/null; then
    ok "~/.ssh/config は設定済み($HOST)"
  else
    {
      echo ""
      echo "$MARK"
      echo "Host $HOST"
      echo "    User $USER_REMOTE"
      echo "    IdentityFile $KEY"
      echo "    IdentitiesOnly yes"
      [ "$PORT" != "22" ] && echo "    Port $PORT"
    } >> "$CFG"
    ok "~/.ssh/config に $HOST のエントリを追加(User=$USER_REMOTE, 鍵=$KEY)"
  fi
fi

# 6. Verify ------------------------------------------------------------------
info "最終確認: パスワードレスログイン"
if passwordless_works; then
  ok "成功。次回からパスワード無しで attach できます。"
  ok "確認: ssh $HOST true / macker <node> で attach"
else
  warn "まだパスワードが必要です。リモートの ~/.ssh 権限(700)や authorized_keys(600)、"
  warn "アカウント名($USER_REMOTE)を確認してください。"
  exit 1
fi
