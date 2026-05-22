#!/usr/bin/env bash
# Onboard the current machine into a macker setup.
# Idempotent: safe to re-run. macOS-focused (Linux: agent runs, no LaunchAgent).
#
# Usage:
#   bash join.sh [--collector URL] [--node NAME] [--launchagent] [--no-install]
#
# Flags:
#   --collector URL   point this node's agent at a collector (hub) URL
#   --node NAME       logical node name (default: hostname)
#   --launchagent     install + load a macOS LaunchAgent (persist across logins)
#   --no-install      skip `go install` (assume macker is already on PATH)
set -euo pipefail

COLLECTOR=""
NODE=""
DO_LAUNCHAGENT=0
DO_INSTALL=1
while [ $# -gt 0 ]; do
  case "$1" in
    --collector) COLLECTOR="${2:-}"; shift 2 ;;
    --node)      NODE="${2:-}"; shift 2 ;;
    --launchagent) DO_LAUNCHAGENT=1; shift ;;
    --no-install)  DO_INSTALL=0; shift ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m[ok]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

# 1. Toolchain ---------------------------------------------------------------
command -v go   >/dev/null 2>&1 || die "Go が必要です (https://go.dev/dl/)"
command -v tmux >/dev/null 2>&1 || warn "tmux が見つかりません。attach/grid に必要: brew install tmux"

# 2. Tailscale ---------------------------------------------------------------
TS="$(command -v tailscale || true)"
[ -z "$TS" ] && [ -x /Applications/Tailscale.app/Contents/MacOS/Tailscale ] \
  && TS=/Applications/Tailscale.app/Contents/MacOS/Tailscale
if [ -z "$TS" ]; then
  warn "Tailscale が見つかりません。Tailscale.app を入れてサインインしてください。"
elif "$TS" status >/dev/null 2>&1; then
  ok "Tailscale: tailnet 参加済み"
else
  warn "Tailscale に未参加のようです。アプリでログインするか 'tailscale up' を実行。"
fi

# 3. Install macker ----------------------------------------------------------
MODULE="github.com/masakasuno1/macker"
GOBIN="$(go env GOBIN)"; [ -z "$GOBIN" ] && GOBIN="$(go env GOPATH)/bin"
# Repo root relative to this script (.../<repo>/.claude/skills/macker-join/scripts).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." 2>/dev/null && pwd || true)"

if [ "$DO_INSTALL" = 1 ]; then
  info "macker をインストール"
  if go install "$MODULE/cmd/macker@latest" 2>/dev/null; then
    ok "go install 完了 ($MODULE)"
  elif [ -f "$REPO_ROOT/go.mod" ] && grep -q "^module $MODULE\$" "$REPO_ROOT/go.mod" 2>/dev/null; then
    warn "公開リポジトリから取得できなかったため、ローカルソースからビルド: $REPO_ROOT"
    ( cd "$REPO_ROOT" && go build -o "$GOBIN/macker" ./cmd/macker )
    ok "ローカルビルド完了 -> $GOBIN/macker"
  else
    die "macker を取得できません(go install 失敗、ローカルソースも見つからない)。--no-install で既存バイナリを使うか、リポジトリ内で実行してください。"
  fi
fi

MACKER="$GOBIN/macker"
[ -x "$MACKER" ] || MACKER="$(command -v macker || true)"
[ -n "$MACKER" ] && [ -x "$MACKER" ] || die "macker が見つかりません ($GOBIN を PATH に通すか --no-install を外す)"
command -v macker >/dev/null 2>&1 || warn "PATH に macker がありません。追加を: export PATH=\"$GOBIN:\$PATH\""
ok "macker: $MACKER ($("$MACKER" version 2>/dev/null || echo '?'))"

# 4. LaunchAgent (macOS only) ------------------------------------------------
if [ "$DO_LAUNCHAGENT" = 1 ]; then
  [ "$(uname -s)" = "Darwin" ] || die "--launchagent は macOS 専用です"
  PLIST="$HOME/Library/LaunchAgents/ai.masao.macker.plist"
  # launchd gives a minimal PATH (/usr/bin:/bin:...), so the agent cannot find
  # tmux (often /opt/homebrew/bin) and `macker ls` shows "(agent?)" with a 500
  # on /v1/sessions. Build a PATH that includes where macker, tmux and the
  # tailscale CLI actually live on this machine.
  AGENT_PATH="/usr/bin:/bin:/usr/sbin:/sbin"
  # Always return 0 — under `set -e`, a missing directory would otherwise abort
  # the script (common on Apple Silicon where /usr/local/bin doesn't exist).
  prepend_path() { case ":$AGENT_PATH:" in *":$1:"*) ;; *) if [ -n "$1" ] && [ -d "$1" ]; then AGENT_PATH="$1:$AGENT_PATH"; fi ;; esac; return 0; }
  prepend_path /usr/local/bin
  prepend_path /opt/homebrew/bin
  command -v brew >/dev/null 2>&1 && prepend_path "$(brew --prefix 2>/dev/null)/bin"
  command -v tmux >/dev/null 2>&1 && prepend_path "$(dirname "$(command -v tmux)")"
  [ -n "$TS" ] && prepend_path "$(dirname "$TS")"
  prepend_path "$GOBIN"

  # Escape values before embedding them in the plist XML so a string
  # containing <, >, & (or a literal </string>) cannot break the document or
  # inject extra launchd keys.
  xml_escape() { printf '%s' "$1" | sed 's/&/\&amp;/g; s/</\&lt;/g; s/>/\&gt;/g'; }
  E_MACKER="$(xml_escape "$MACKER")"
  E_PATH="$(xml_escape "$AGENT_PATH")"
  E_COLLECTOR="$(xml_escape "$COLLECTOR")"
  E_NODE="$(xml_escape "$NODE")"

  # TERM must always be set: the Mac App Store Tailscale CLI prints
  # "The Tailscale GUI failed to start" (not JSON) to stdout when TERM is
  # unset, so the agent fails to read tailnet status and binds loopback only,
  # invisible to other nodes. launchd does not pass TERM, so we set it here.
  #
  # TMUX_TMPDIR pins the tmux socket directory to /tmp. The agent (in the GUI
  # launchd session) and the `ssh <node> tmux attach` an attaching client runs
  # must use the SAME tmux server; without a fixed TMUX_TMPDIR they can diverge
  # on macOS (per-session $TMPDIR), so the agent creates a session the ssh side
  # cannot find. ~/.zshenv below pins the same value for non-interactive ssh.
  ENVBLOCK="    <key>EnvironmentVariables</key>
    <dict>
      <key>TERM</key><string>xterm-256color</string>
      <key>TMUX_TMPDIR</key><string>/tmp</string>
      <key>PATH</key><string>$E_PATH</string>"
  [ -n "$COLLECTOR" ] && ENVBLOCK="$ENVBLOCK
      <key>MACKER_COLLECTOR</key><string>$E_COLLECTOR</string>"
  [ -n "$NODE" ] && ENVBLOCK="$ENVBLOCK
      <key>MACKER_NODE</key><string>$E_NODE</string>"
  ENVBLOCK="$ENVBLOCK
    </dict>"
  mkdir -p "$HOME/Library/LaunchAgents"
  cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
  <dict>
    <key>Label</key><string>ai.masao.macker</string>
    <key>ProgramArguments</key>
    <array>
      <string>$E_MACKER</string>
      <string>agent</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ProcessType</key><string>Interactive</string>
    <key>StandardOutPath</key><string>/tmp/macker-agent.log</string>
    <key>StandardErrorPath</key><string>/tmp/macker-agent.log</string>
$ENVBLOCK
  </dict>
</plist>
EOF
  launchctl unload "$PLIST" >/dev/null 2>&1 || true
  launchctl load "$PLIST"
  ok "LaunchAgent をロード: $PLIST (ログ: /tmp/macker-agent.log)"
  sleep 1
else
  info "LaunchAgent は未設定。試すなら別ターミナルで:"
  if [ -n "$COLLECTOR" ]; then
    echo "    MACKER_COLLECTOR=$COLLECTOR ${NODE:+MACKER_NODE=$NODE }macker agent"
  else
    echo "    ${NODE:+MACKER_NODE=$NODE }macker agent"
  fi
fi

# 4b. Attach prerequisites over ssh -----------------------------------------
# `macker <node>` attaches by running `ssh <node> tmux attach-session ...`. A
# non-interactive ssh shell does NOT source ~/.zshrc, so on Apple Silicon tmux
# (/opt/homebrew/bin) is off PATH and the attach fails with
# "command not found: tmux". It also needs the SAME tmux socket dir as the
# agent (see TMUX_TMPDIR above). ~/.zshenv IS sourced for non-interactive zsh,
# so pin both there. Idempotent: only appended once.
if [ "$(uname -s)" = "Darwin" ] && command -v tmux >/dev/null 2>&1; then
  TMUX_DIR="$(dirname "$(command -v tmux)")"
  ZENV="$HOME/.zshenv"
  if ! grep -q "macker: attach over ssh" "$ZENV" 2>/dev/null; then
    {
      echo ""
      echo "# macker: attach over ssh needs tmux on PATH and a fixed socket dir"
      echo "export PATH=\"$TMUX_DIR:\$PATH\""
      echo "export TMUX_TMPDIR=/tmp"
    } >> "$ZENV"
    ok "~/.zshenv に tmux PATH と TMUX_TMPDIR=/tmp を追加(ssh attach 用)"
  else
    ok "~/.zshenv は設定済み(ssh attach 用)"
  fi
  # Remote Login (sshd) must be on for other nodes to attach here. We cannot
  # enable it headlessly (it needs admin), so just report its state.
  if launchctl print system/com.openssh.sshd >/dev/null 2>&1; then
    ok "リモートログイン(SSH): 有効"
  else
    warn "リモートログイン(SSH)が無効のようです。他ノードから attach するには"
    warn "  システム設定 → 一般 → 共有 → リモートログイン を ON にしてください。"
  fi
fi

# 5. Verify ------------------------------------------------------------------
info "確認: macker ls"
"$MACKER" ls || warn "ls 失敗。agent が起動しているか確認してください。"
ok "完了。'macker ls' で全ノードを、'macker grid <node>...' でグリッドを開けます。"
