# macker-join 詳細ガイド

## join.sh のフラグ

| フラグ | 意味 |
|--------|------|
| `--collector URL` | この agent をハブ(collector)へ接続し、イベントを転送する |
| `--node NAME` | 論理ノード名(既定: ホスト名) |
| `--launchagent` | macOS の LaunchAgent を生成・ロードして agent を常駐させる |
| `--no-install` | `go install` を省略(既に macker が PATH にある前提) |

環境変数 `MACKER_COLLECTOR` / `MACKER_NODE` でも指定可。`--launchagent` 指定時は
それらが plist の `EnvironmentVariables` に書き込まれる。

## なぜ LaunchAgent なのか(LaunchDaemon ではなく)

agent は GUI ログインセッション内で動く必要がある。macOS の login Keychain は GUI
ログイン時にアンロックされ、素の SSH の非 GUI セッションからは触れない。GUI 内で
動いている tmux に attach することで、ふだん画面前で作業しているのと同じ
(正規にアンロック済みの)環境を使える。LaunchDaemon(ログイン前/root)では
Keychain も tmux サーバも見えないため不可。

## トラブルシューティング

- **`macker: command not found`**: `$(go env GOPATH)/bin` を PATH に追加
  (`export PATH="$(go env GOPATH)/bin:$PATH"`)。
- **Tailscale 未参加**: Tailscale.app でログイン、または `tailscale up`。
  CLI が見つからない場合は `MACKER_TAILSCALE_BIN` でパス指定。
- **agent ログ**: LaunchAgent の場合 `/tmp/macker-agent.log`。
  `launchctl list | grep macker` で稼働確認。
- **再ロード**: `launchctl unload ~/Library/LaunchAgents/ai.masao.macker.plist`
  してから `load`(join.sh 再実行でも可)。
- **collector に届かない**: collector ノードで `macker collector` が起動し、
  ポート(既定 :4478)に tailnet 越しで到達できるか確認。
- **agent が loopback のみにバインド(他ノードから見えない)**: ログに
  `tailnet status unavailable, binding loopback only` が出る場合、App Store 版
  Tailscale CLI が `TERM` 未設定だと `status --json` の代わりに
  「The Tailscale GUI failed to start」を返すのが原因。launchd は `TERM` を
  渡さないため、plist の `EnvironmentVariables` に `TERM`(任意の値、例
  `xterm-256color`)を入れる。join.sh は `--launchagent` 時に自動で付与する。
- **`macker ls` の SESSIONS が `(agent?)` / `/v1/sessions 500`**: launchd の
  最小 PATH に tmux(多くは `/opt/homebrew/bin`)が無く、agent が tmux を起動
  できないのが原因。plist の `EnvironmentVariables` に tmux のあるディレクトリを
  含む `PATH` を入れる。join.sh は `--launchagent` 時に tmux / tailscale /
  macker の場所を拾って自動で付与する。

## attach(`macker <node>`)が動かないとき

attach は `ssh <node> tmux attach-session ...` でリモートの tmux につなぐ。
agent の認可(:4477)とは別に、以下のノード側・クライアント側の条件が要る。

- **`command not found: tmux`(ssh 経由)**: 非対話 ssh は `~/.zshrc` を読まない
  ため、Apple Silicon の tmux(`/opt/homebrew/bin`)が PATH から外れる。
  `~/.zshenv` に `export PATH="/opt/homebrew/bin:$PATH"` を追加(join.sh が自動で
  行う)。
- **`agent returned 400: ... duplicate session`**: agent の `list-sessions` が
  launchd セッション配下で空を返す一方、`new-session` は既存セッションを検出する
  ことがある(macOS の per-session tmux サーバ可視性)。macker 本体はセッション
  作成を冪等化(`duplicate session` を成功扱い)してこれを吸収する。古いバイナリ
  なら各ノードを最新に更新する。
- **`zsh:1: <session> not found`**: リモート zsh が `=<session>`(exact-match の
  `=` 接頭辞)をファイル名展開と誤解釈する。macker 本体は attach のリモート
  コマンドをシングルクォートして渡し、これを防ぐ。古いバイナリなら更新する。
- **`missing or unsuitable terminal: <TERM>`**: クライアントの `TERM`(例
  Ghostty の `xterm-ghostty`)の terminfo がリモートに無い。クライアント側で
  一度だけ:`infocmp -x $TERM | ssh <node> 'tic -x -'`。
- **`<user>@<node>: Permission denied` / Password を聞かれる**: attach の ssh は
  **ローカルのユーザ名**で接続する。リモートのアカウント名が違う場合、
  クライアントの `~/.ssh/config` でホスト→ユーザ/鍵を対応づける:
  ```
  Host <node>.<tailnet>.ts.net
      User <remote-account>
      IdentityFile ~/.ssh/id_ed25519
      IdentitiesOnly yes
  ```
- **`port 22: Connection refused`**: リモートで「リモートログイン(SSH)」が無効。
  そのマシンの システム設定 → 一般 → 共有 → リモートログイン を ON。
- **tmux socket の不一致**: agent と ssh が別 socket を使うとセッションが見えない。
  両方で `TMUX_TMPDIR=/tmp` に固定する(join.sh が plist と `~/.zshenv` に設定)。

## 既知の制限

- `macker ls` の SESSIONS が、attach できているのに `none` のままになることがある。
  agent(GUI launchd セッション)の `tmux list-sessions` が、別セッション由来の
  tmux サーバを取りこぼすため。attach 自体は冪等な作成で動く。根本解決には
  全 tmux 操作で固定 socket(`tmux -S <path>`)を使う改修が必要(follow-up 候補)。
