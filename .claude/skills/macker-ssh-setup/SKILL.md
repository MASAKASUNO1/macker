---
name: macker-ssh-setup
description: macker のノードへ鍵ベースのパスワードレス ssh を設定するスキル。クライアント側で実行し、ローカル鍵生成 → 公開鍵配布(初回だけパスワード) → ~/.ssh/config 設定 → 疎通確認まで案内する。「ssh のパスワードを毎回聞かれる」「自動ログインにしたい」「鍵を作って配って」「macker の Permission denied / パスワードを聞かれる」で発動。
---

# macker-ssh-setup

`macker <node>` の attach は `ssh <node> tmux attach` で動くので、ssh が
パスワードレス(鍵)でないと毎回パスワードを聞かれる。このスキルはその初期設定を
**接続する側(クライアント)** で一度だけ行う。

## WHAT

`scripts/ssh-setup.sh` が冪等に設定する:

1. ローカルの ssh 鍵を確認(無ければ ed25519 を生成)
2. 既に鍵でログインできるなら、配布をスキップして終了
3. 公開鍵をリモートへ配布(`ssh-copy-id`。**ここで一度だけ**リモートの
   パスワードを ssh 自身が聞く。スクリプトはパスワードを読まない/保存しない)
4. `~/.ssh/config` に冪等なブロックを追加(Host→User/IdentityFile/IdentitiesOnly)
5. 非対話・鍵のみでログインできることを確認

リモートの sshd 設定(`PasswordAuthentication`)は**変更しない**。鍵ログインを
足すだけで、パスワード認証はそのまま残る。

## WHY

macker 本体は ssh 認証に一切関与せず、OS の ssh 設定に委ねている
(`internal/attach/attach.go` は `ssh -tt <addr> "exec tmux attach..."` を呼ぶだけ)。
そのため鍵生成・公開鍵配布・`~/.ssh/config` のユーザ対応は従来すべて手作業で、
`macker-join`(ノード側の整備)ではカバーしていなかった。「agent は見えるのに
attach のたびにパスワードを聞かれる」体験を、初回の一手順で解消する。

## HOW

```sh
# MagicDNS 名(or IP)とリモートのアカウント名を指定して設定
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh \
  --host mac-mini.your-tailnet.ts.net --user masao

# macker のノード名から Tailscale でアドレスを自動解決(tailscale + python3 が要る)
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh --node mac-mini --user masao

# 既にパスワードレスか確認だけする
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh --host mac-mini.your-tailnet.ts.net --test-only
```

実行すると、配布のステップでリモートのパスワードを **一度だけ** 求められる
(以降は鍵で自動)。ユーザーには「ここでパスワードを入力するのは初回だけ」と
伝える。リモートのアカウント名(`--user`)が不明なら聞く。`--host` が不明で
Tailscale が使えるなら `--node` で解決できる。

設定後、`ssh <host> true` がパスワード無しで通り、`macker <node>` の attach も
パスワードを聞かれなくなる。なお `macker <node>` で attach する際は、鍵だけでなく
tmux PATH / `TMUX_TMPDIR` / リモートログイン / terminfo も要る — そちらは
`macker-join` の `reference.md` を参照。

## Resources

- 詳細・トラブルシュート: `reference.md`
- 使用例: `examples.md`
- 本体スクリプト: `scripts/ssh-setup.sh`
- 関連: ノード側の整備は `macker-join` スキル
