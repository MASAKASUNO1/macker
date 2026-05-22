# macker-ssh-setup 詳細ガイド

## ssh-setup.sh のフラグ

| フラグ | 意味 |
|--------|------|
| `--host HOST` | ssh 接続先。MagicDNS 名(`node.tailnet.ts.net`)か Tailscale IP |
| `--node NAME` | macker の論理ノード名。Tailscale が使えれば `--host` を自動解決 |
| `--user USER` | リモートのアカウント名(既定: ローカルの `$USER`) |
| `--key PATH` | 秘密鍵のパス(既定: `~/.ssh/id_ed25519`) |
| `--port N` | ssh ポート(既定: 22) |
| `--ask-passphrase` | 鍵生成時にパスフレーズを対話入力(既定: パスフレーズ無し) |
| `--no-config` | `~/.ssh/config` のブロックを書かない |
| `--test-only` | 鍵ログインが既に可能か確認するだけ(何も変更しない) |

## なぜクライアント側で実行するのか

attach の ssh は **接続する側のローカルユーザ名** で、**ローカルの鍵** を使って
リモートにつなぐ。したがって鍵生成・`~/.ssh/config` はクライアント側の設定であり、
ノードを macker に参加させる `macker-join`(ノード側の sshd / PATH / agent 整備)
とは責務が分かれる。「A から B へつなぐ」なら A で本スクリプトを実行する。

## パスワードはどう扱われるか

パスワードを聞くのは公開鍵配布(`ssh-copy-id`)の**一度だけ**で、入力を受けるのは
ssh 本体。本スクリプトはパスワードを読み取らず、変数にも履歴にも残さない。配布が
済めば以降は鍵認証になるため、パスワードは再び要求されない。

## パスフレーズと自動ログインのトレードオフ

- 既定(パスフレーズ無し): 2回目以降が完全にハンズフリー。鍵ファイルが漏れると
  そのまま悪用されるリスクがある。個人 tailnet 内の利用なら一般的に許容範囲。
- `--ask-passphrase`: パスフレーズ付き鍵を作る。macOS では本スクリプトが
  `ssh-add --apple-use-keychain` で Keychain に登録するため、ログイン後は実質
  ハンズフリーのまま安全性を上げられる。

## sshd は変更しない

リモートの `PasswordAuthentication` などの sshd 設定は触らない。鍵ログインを
追加するだけで、パスワード認証はそのまま残る(締め出し事故を避けるため)。
パスワード認証を止めたい場合は、鍵ログインが確実に動くことを `--test-only` で
確認してから、リモートで手動で `/etc/ssh/sshd_config` を編集する。

## ~/.ssh/config に書かれる内容

```
# macker: passwordless attach to <host>
Host <host>
    User <remote-account>
    IdentityFile ~/.ssh/id_ed25519
    IdentitiesOnly yes
    # Port は 22 以外のときだけ追記
```

マーカーコメントで冪等性を担保し、同じ host のブロックが既にあれば追記しない。
`IdentitiesOnly yes` は、agent に多数の鍵があるとき「Too many authentication
failures」になるのを防ぐ。

## トラブルシューティング

- **`ssh-copy-id` で `Permission denied`(配布段階)**: リモートのアカウント名
  (`--user`)かパスワードが違う。リモートで「リモートログイン(SSH)」が有効か
  (macOS は システム設定 → 一般 → 共有 → リモートログイン)も確認。
- **`port 22: Connection refused`**: リモートで sshd が動いていない。上記の
  リモートログインを ON にする。
- **配布したのにまだパスワードを聞かれる**: リモートの権限が原因のことが多い。
  `~/.ssh` が 700、`~/.ssh/authorized_keys` が 600、ホームディレクトリが
  group/other 書き込み不可であること。確認は `--test-only`。
- **`Host key verification failed`**: 既知ホストの鍵が変わった。
  `ssh-keygen -R <host>` で古い項目を消してから再実行。
- **`--node` が解決できない**: `tailscale` CLI と `python3` が要る。見つからない/
  名前が一致しない場合は `--host` に MagicDNS 名か IP を直接渡す。
- **複数ノードに一括設定したい**: ノードごとに `--host`(または `--node`)を変えて
  本スクリプトを繰り返す。`~/.ssh/config` はノード単位で冪等に追記される。

## attach がまだ動かないとき(鍵以外の要因)

鍵が通っても `macker <node>` の attach には別の足回りが要る
(`command not found: tmux` / `unsuitable terminal: <TERM>` / tmux socket 不一致 /
リモートログイン無効 など)。これらは `macker-join` スキルの `reference.md`
「attach が動かないとき」に集約している。
