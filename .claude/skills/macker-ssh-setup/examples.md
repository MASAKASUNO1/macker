# macker-ssh-setup 使用例

## 1. 基本: ホスト名とリモートユーザを指定

`mac-mini` に `masao` で鍵ログインを設定する(配布時に一度だけパスワード入力)。

```sh
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh \
  --host mac-mini.your-tailnet.ts.net --user masao
```

出力イメージ:

```
==> 現状を確認: masao@mac-mini... で鍵ログインできるか
==> 公開鍵をリモートへ配布(ここでリモートのパスワードを一度だけ聞かれます)
masao@mac-mini.your-tailnet.ts.net's password:        # ← 初回だけ
[ok] 公開鍵を配布しました。
[ok] ~/.ssh/config に mac-mini.your-tailnet.ts.net のエントリを追加
==> 最終確認: パスワードレスログイン
[ok] 成功。次回からパスワード無しで attach できます。
```

## 2. macker のノード名から解決

MagicDNS 名がうろ覚えでも、Tailscale + python3 があればノード名で引ける。

```sh
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh --node mac-mini --user masao
```

## 3. 確認だけ(変更なし)

既にパスワードレスかをチェック。CI 的に使える(成功で exit 0)。

```sh
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh \
  --host mac-mini.your-tailnet.ts.net --test-only
```

## 4. ローカルとリモートでユーザ名が同じとき

`--user` は省略でき、ローカルの `$USER` が使われる。

```sh
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh --host work-laptop.your-tailnet.ts.net
```

## 5. パスフレーズ付き鍵で安全性を上げる

鍵生成時にパスフレーズを設定。macOS では Keychain に登録され、ログイン後は
ハンズフリーのまま使える。

```sh
bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh \
  --host mac-mini.your-tailnet.ts.net --user masao --ask-passphrase
```

## 6. 複数ノードへまとめて

ノードごとに繰り返す。`~/.ssh/config` はノード単位で冪等に追記される。

```sh
for h in mac-mini.your-tailnet.ts.net studio.your-tailnet.ts.net; do
  bash .claude/skills/macker-ssh-setup/scripts/ssh-setup.sh --host "$h" --user masao
done
```

## 設定後の流れ

```sh
ssh mac-mini.your-tailnet.ts.net true   # パスワード無しで通る
macker mac-mini                          # attach もパスワードを聞かれない
```

attach がまだ動かない場合(tmux PATH / TERM / リモートログインなど鍵以外の要因)は
`macker-join` の `reference.md` を参照。
