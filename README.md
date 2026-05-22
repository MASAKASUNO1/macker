# macker

自分が所有する複数のマシン(据え置き Mac、MacBook、Mac mini など)で動く
ターミナル/開発セッションを、Tailscale tailnet 越しに1つの CLI からまとめて
管理するツール。自分の tailnet・自分のマシンの中だけで動く、個人用の
リモート開発セッション管理です。

macker は **tmux + Tailscale の上に乗る薄いコントロールプレーン**です。ターミナル
多重化やトランスポート暗号化は再実装しません(端末ストリームは `ssh`+`tmux`、
メンバーシップ・identity・暗号化は Tailscale に委譲)。その上に、セッションの
*可視化*、ワンコマンド *attach*、ノードでの認可付きコマンド実行、追記専用の
*監査ログ*、複数ノードを並べる *グリッド* を足しています。長時間動く開発作業
(ビルド、対話シェル、エージェント的なツールなど)を別マシンで走らせ、手元の
1ウィンドウから見渡すのが狙いです。

アーキテクチャと設計判断は `DESIGN.md` を参照。

## なぜ tmux に attach するのか

これはセキュリティの回避策ではありません。**自分のマシンに、自分が正規に
ログインしている状態で、リモートからアクセスするための実務的な工夫**です。

macOS の login Keychain は GUI でログインしたときにアンロックされます。一方、
素の `ssh` で入った非 GUI セッションはその Keychain にアクセスできないため、
GUI から起動して認証情報を Keychain に持つ種類のツール全般(Claude Code に限らず)が
リモートからは正しく動かないことがあります。これは macOS 一般の挙動で、特定の
ツールの認証を破るものではありません。

macker は、**すでに自分の GUI ログインセッション内で動いている tmux サーバに
attach** します。そこは正規にアンロック済みの環境なので、ふだん画面の前で
作業しているのと同じ状態がそのまま使えます。これが、agent を(ログイン前の
LaunchDaemon ではなく)**LaunchAgent**(GUI セッション内)で動かす理由でもあります。

## インストール

公開リポジトリから直接:

```sh
go install github.com/masakasuno1/macker/cmd/macker@latest
```

`$(go env GOPATH)/bin` に `macker` が入ります。そこへ PATH を通してください
(ソースからのビルド手順は下の「はじめかた」2 を参照)。

## はじめかた

**各マシンで一度だけ:**

1. 各マシンに Tailscale を入れてサインインし、全マシンを同じ tailnet に参加させる。
   macOS なら **Tailscale.app を入れて GUI でログインするだけ**で OK(CLI 派なら
   `tailscale up`)。macker は `tailscale` CLI を `Tailscale.app` のバンドル内からも
   自動検出するので、アプリだけ入っていれば別途 CLI を用意する必要はない
   (見つからない場合は `MACKER_TAILSCALE_BIN` でパス指定可)。
2. `macker` 実行ファイルを各マシンに用意し、`PATH` の通った場所に置く。方法は2つ:

   - **公開リポジトリから直接インストール(各マシンで楽)**:
     ```sh
     go install github.com/masakasuno1/macker/cmd/macker@latest
     # $(go env GOPATH)/bin に macker が入る。そこに PATH を通しておく:
     #   export PATH="$(go env GOPATH)/bin:$PATH"
     ```
   - **ソースからビルド**(このリポジトリをクローンして、ルートで):
     ```sh
     go build -o macker ./cmd/macker   # ルートに ./macker が1個できるだけ
     sudo mv macker /usr/local/bin/     # PATH の通った場所へ自分で移す
     ```
3. ノードデーモンを起動する。macOS では **LaunchAgent** として入れて、GUI ログイン
   セッション内で動かす(Keychain アクセスに必須。下記参照):
   ```sh
   cp example/ai.masao.macker.plist ~/Library/LaunchAgents/
   # plist 内の macker バイナリのパスを自分の環境に合わせて書き換えてから:
   launchctl load ~/Library/LaunchAgents/ai.masao.macker.plist
   ```
   とりあえず試すだけなら、ターミナルで `macker agent` を直接動かしてもよい。
4. (任意)ハブにする1台で `macker collector` も動かすと、どのノードで何が走った
   かの履歴を中央に集約できる。

**あとは普段の操作(どのマシンからでも):**

```sh
macker ls                       # 誰がオンラインで何が動いているか
macker mac-mini                 # そのマシンで新しいセッションを開く(ウィンドウ1つにつき1セッション)
macker mac-mini:dev             # 名前付きの再 attach 可能なセッションに入る(無ければ作成)
macker mac-mini ls              # そのマシンのセッションを詳細表示(clear/attach の判断用)
macker mac-mini:dev clear       # そのセッションをリセット(次の attach は新規)
macker exec macbook -- git pull # ノード上で認可付きコマンドを1つ実行
```

`macker <マシン名>` がそのまま attach です(`attach` と打つ必要はありません)。
素の `macker <node>` はウィンドウごとに独立した新規セッションを開き、その窓を
閉じるとそのセッションだけが kill されます。続きをやり直したいセッションは
`<node>:<session>` の名前付きにしておくと、detach・再 attach できます。

**tab 補完(zsh)** を入れておくと、サブコマンド・ライブなノード名・`<node>:` の
後のセッション名まで補完されます:

```sh
eval "$(macker completion zsh)"   # ~/.zshrc の `compinit` の後に追記
```

**最初に話していたユースケース — 1ウィンドウに複数マシンのグリッド:**

```sh
macker grid self mac-mini macbook mac-mini-2
```

4つのペインがタイル表示され、各ペインが別マシンのセッションに attach します
(自分のマシンが左上、ほかが周りに)。ctrl+c を連打する(または窓を閉じる)と
そのマシンのセッションが終了、ラップトップを sleep しても全部生き残るので後で
再 attach できます。

## コマンド

```
macker <node>                     ノードで新しいセッションを開く(ウィンドウ1つにつき1つ。
                                  閉じるとそのセッションだけが kill される)
macker <node>:<session>           名前付きの再 attach 可能なセッションに attach(無ければ作成)
macker <node>[:<session>] clear   そのセッションをリセット(kill。次の attach は新規)
macker ls                         ノードとそのセッション一覧(状態つき)
macker <node> ls                  1ノードのセッションを詳細表示(clear/attach の判断用)
macker exec <node> -- <cmd>...    ノードで認可付きコマンドを実行
macker grid <target>...           各ターゲットに attach したグリッドを開く
macker agent                      ノードデーモンを起動
macker collector                  イベント収集デーモンを起動
macker context [ls|use <name>]    アクティブなコンテキストの表示・切替
macker completion zsh             zsh タブ補完スクリプトを出力
macker version                    バージョンを表示
```

明示形 `macker attach <node>` / `macker kill <node>:<session>` も使えます。

グローバルフラグ `--context <name>`(または `MACKER_CONTEXT`)で config の
コンテキストを選択。

`<node>` は tailnet のノード名。`self` / `local` / 空のノードはこのマシンを指す
(ループバック経由で到達するので、Tailscale 設定前でも使える)。

### セッションのライフサイクル(DESIGN.md §4)

セッションが kill されるのは **明示的な意思表示のときだけ**:

- **ctrl+c の連打**(400ms 以内に3回)、または
- ターミナルの窓を閉じる(SIGHUP/SIGTERM)。

接続断やラップトップの sleep ではセッションは死なず、生き残って再 attach できます。
`--keep` を付けると、明示的に閉じても kill ではなく detach だけになります。

`macker ls` は各セッションの状態を表示します。状態は、attach クライアントが agent に
heartbeat する lease から導出します:

- **attached** — 有効な lease(または素の tmux クライアント)が attach 中;
- **orphaned** — ephemeral セッションで、クリーンな終了なしに holder が消えた
  (sleep/クラッシュ)。`macker kill` で掃除する;
- **detached** — 生存しているが誰も attach していない(クリーン detach / 外部生成 / --keep)。

## グリッド

`macker grid n1 n2 n3 n4` は、専用ソケットの tmux グリッドにターゲットごとの
ペインをタイル表示し、各ペインにターゲット名のタイトルを付けます。`--layout` で
tmux のレイアウトを選べます。実験的な `--mode windows`(macOS)は、ターゲットごとに
ネイティブの端末ウィンドウ(Ghostty / iTerm / Terminal)を開きます(未対応の端末では
tmux グリッドにフォールバック)。

新しいセッションは作成時に、**ノード名から決まる色でステータスバーが色付け**され、
左端にノード名のラベルが付きます(ベストエフォート)。同じマシンのセッションは同じ色に、
別マシンは別の色になるので、グリッドや複数窓でいま自分がどのマシンにいるかを一目で
判別できます。

## コレクター

1台で `macker collector` を動かし、ほかのノードの config(または
`MACKER_COLLECTOR`)に `collector` を設定します。各 agent は自分の追記専用ログを
コレクターへ転送し、コレクターは `<tenant>/<node>.jsonl` にイベントをミラーします。
コレクターはあくまでミラーで、落ちている間 agent はローカルにバッファし、復帰時に
リプレイするので、イベントは失われません。

## コンテキスト(マルチテナント)

*テナント* = tailnet です。config ファイルの `contexts` で、1台のマシンが複数の
tailnet を、状態を混ぜずにターゲットにできます:

```json
{
  "current_context": "work",
  "contexts": {
    "work": { "tailscale_bin": "/usr/local/bin/tailscale", "collector": "http://hub.work.ts.net:4478" },
    "home": { "policy": { "owners": ["me@example.com"] } }
  }
}
```

`macker context use <name>` で切り替え、または `--context <name>` でコマンド単位で
指定。非デフォルトのコンテキストは `…/macker/contexts/<name>` に状態を分離します。

## agent(デーモン)

各ノードで `macker agent` を動かします。macOS では LaunchAgent として入れて、GUI
ログインセッション内に常駐させます — `example/ai.masao.macker.plist` を参照。

### 認可

ケイパビリティは、呼び出し元ピアの Tailscale identity(`tailscale whois`)から
決まります(Tailscale 自体のメンバーシップ・暗号化の上に重ねる):

- **ループバック**の呼び出しはローカルオーナー(フルアクセス)。ただしローカル
  トークン(`DataDir/agent.token`、0600)の提示が必要 — 同一マシンの別ローカル
  ユーザーによる乗っ取りを防ぐ;
- **同じ tailnet アカウントが所有する別デバイス**(`tailscale whois` の login が
  この agent 自身の login と一致)は自動で **CapExec**。自分の複数マシンは
  per-node 設定なしで相互に exec・作成・kill できる(個人用の既定挙動);
- それ以外のリモートピアは config の `policy` でケイパビリティが決まる:
  - `owners` / `exec_allow` → exec・作成・kill 可(CapExec);
  - `attach_allow`(空 = 任意の tailnet ピア)→ list/attach 可(CapAttach)。

exec と認可拒否はすべて追記専用イベントログに記録されます。

## 設定

`~/.config/macker/config.json`(全フィールド任意。`example/config.json` 参照):

```json
{
  "node": "mac-mini",
  "listen": ":4477",
  "collector": "",
  "policy": {
    "owners": ["you@example.com"],
    "exec_allow": [],
    "attach_allow": []
  }
}
```

環境変数による上書き: `MACKER_NODE`、`MACKER_LISTEN`、`MACKER_TAILSCALE_BIN`、
`MACKER_DATA_DIR`、`MACKER_COLLECTOR`、`MACKER_CONFIG`、`MACKER_CONTEXT`。

## ステータス

エンドツーエンドで動作(初期段階だが機能する)。実装済み:

- agent デーモン(health / sessions / exec / lease / release / kill / events)。
  ループバックトークン + `tailscale whois` 認可、追記専用の監査ログつき;
- `ls` / `attach` / `exec` / `kill` / `grid` / `collector` / `context`;
- ウィンドウごとに独立した新規セッション(素の `macker <node>`)と、再 attach 可能な
  名前付きセッション(`<node>:<session>`)、`<node> ls` での詳細一覧;
- ctrl+c 連打 + 窓を閉じるライフサイクル、lease ベースの
  `attached`/`orphaned`/`detached` 状態;
- ノード名から決まるステータスバーの色付け(どのマシンにいるか一目で判別);
- zsh タブ補完(`macker completion zsh` / 動的なノード・セッション補完);
- バッファリング shipper つきの collector ミラーリング(落ちてもイベント無損失);
- マルチテナントコンテキスト(`--context`、コンテキストごとに状態分離)。

既知の制限(`DESIGN.md` §8 参照): collector のテナント境界はフラット(CapExec を
持つプリンシパルは他テナントも照会できる)、`/v1/collect` はイベントの素性を検証せず
shipper を信頼する。トランスポートは HTTP/JSON(gRPC 未対応)。

テスト: `go test -race ./...`(58 テスト)+ エンドツーエンドのスモーク確認。
