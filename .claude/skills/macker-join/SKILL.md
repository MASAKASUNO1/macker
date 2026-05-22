---
name: macker-join
description: 新しいマシンを macker のセットアップに参加(onboarding)させるスキル。Tailscale 確認 → macker インストール → agent 起動(任意で LaunchAgent)→ 動作確認までを案内する。「macker に参加」「このマシンを macker に追加」「macker をセットアップ」「macker join / onboarding」で発動。
---

# macker-join

このマシンを既存の macker セットアップ(同じ Tailscale tailnet)に参加させる。

## WHAT

`scripts/join.sh` が冪等に onboarding を行う:

1. ツールチェーン確認(Go / tmux)
2. Tailscale 参加確認(`tailscale status`。アプリでログイン済みなら OK)
3. `go install github.com/masakasuno1/macker/cmd/macker@latest` で macker を導入
4. (任意)macOS の LaunchAgent を生成・ロードして agent を常駐
   (plist に `TERM` / `TMUX_TMPDIR=/tmp` / tmux 入りの `PATH` を付与)
5. attach 前提の整備:`~/.zshenv` に tmux の PATH と `TMUX_TMPDIR=/tmp` を追加
   (非対話 ssh から `tmux attach` が動くように)、リモートログイン状態を報告
6. `macker ls` で参加を確認

## WHY

参加に必要な手順(Tailscale・インストール・Keychain のための LaunchAgent・collector
への接続)は地味に多く間違えやすい。1コマンドにまとめ、再実行しても壊れないようにする。
さらに `macker <node>` の attach は `ssh <node> tmux attach` で動くため、非対話 ssh の
PATH・tmux socket(`TMUX_TMPDIR`)・リモートログイン・terminfo といった足回りも要る。
ここがズレると「agent は見えるのに attach できない」になりやすいので、ノード側でできる
分は join.sh が用意し、クライアント側(ssh 鍵 / `~/.ssh/config` のユーザ対応 / terminfo)は
`reference.md` に集約する。

## HOW

```sh
# 試す(LaunchAgent なし。別ターミナルで macker agent を促す)
bash .claude/skills/macker-join/scripts/join.sh

# 常駐 + ハブ(collector)に接続して本参加(macOS)
bash .claude/skills/macker-join/scripts/join.sh \
  --launchagent --collector http://hub.your-tailnet.ts.net:4478 --node my-mac
```

実行後、ユーザーに `macker ls` の出力を見せ、未参加なら Tailscale ログインや
PATH 追加を案内する。collector URL やノード名が不明なら聞く。

`macker <node>` で attach できない場合は agent の問題と切り分ける。多くは attach 経路
(ssh)側で、`reference.md` の「attach が動かないとき」を参照:`command not found: tmux`
/ `duplicate session` / `=<session> not found` / `unsuitable terminal: <TERM>` /
`Permission denied`(ユーザ名/鍵)/ `port 22: Connection refused`(リモートログイン)。
クライアントが Ghostty 等の独自 TERM なら `infocmp -x $TERM | ssh <node> 'tic -x -'` を
一度実行。リモートのアカウント名がローカルと違うなら `~/.ssh/config` で対応づける。

## Resources

- 詳細・トラブルシュート: `reference.md`
- 使用例: `examples.md`
- 本体スクリプト: `scripts/join.sh`
