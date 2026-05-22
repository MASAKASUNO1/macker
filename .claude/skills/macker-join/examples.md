# macker-join 使用例

## まず試す(常駐なし)

```sh
bash .claude/skills/macker-join/scripts/join.sh
```

Go / tmux / Tailscale を確認し、macker をインストールして `macker ls` まで実行。
agent は別ターミナルで `macker agent` を促される。

## 本参加(常駐 + ハブ接続、macOS)

```sh
bash .claude/skills/macker-join/scripts/join.sh \
  --launchagent \
  --collector http://hub.tailXXXX.ts.net:4478 \
  --node macbook
```

LaunchAgent を生成・ロードして agent を常駐させ、イベントをハブの collector へ転送。

## 既に macker がある場合(再インストール不要)

```sh
bash .claude/skills/macker-join/scripts/join.sh --no-install --launchagent
```

## このスキルの呼ばれ方

ユーザーが「このマシンを macker に参加させて」「macker をセットアップ」などと言うと
発動。Claude は join.sh を実行し、`macker ls` の出力を見せ、未参加(Tailscale 未ログイン、
PATH 未設定)なら具体的な対処を案内する。collector URL / node 名が不明なら確認する。
