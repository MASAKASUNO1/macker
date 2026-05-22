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
5. `macker ls` で参加を確認

## WHY

参加に必要な手順(Tailscale・インストール・Keychain のための LaunchAgent・collector
への接続)は地味に多く間違えやすい。1コマンドにまとめ、再実行しても壊れないようにする。

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

## Resources

- 詳細・トラブルシュート: `reference.md`
- 使用例: `examples.md`
- 本体スクリプト: `scripts/join.sh`
