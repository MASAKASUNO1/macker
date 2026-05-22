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
