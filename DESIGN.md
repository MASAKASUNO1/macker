# macker — 設計ドキュメント (draft)

Tailnet 内の複数マシン上で動く AI エージェント/開発セッションを、1つの CLI から
可視化・attach・操作するためのコントロールプレーン。

> 位置づけ: 自分専用ツールとして始めるが、最初からサービス化できる構造で作る。
> tmux + Tailscale の「上に乗る」薄いコントロールプレーンに徹し、ターミナル多重化や
> トランスポート暗号化など既存が強い部分は自作しない。

## 1. レイヤー構成

```
[Discovery/Liveness]  Tailscale に委譲(デバイス一覧・到達性・MagicDNS・identity)
[Control plane]       各ノードの macker agent(セッション報告 / attach / exec)
[Collector]           履歴・監査ログの集約ミラー(単一・落ちても機能停止しない)
```

- ノード一覧/オンライン状態/名前解決は Tailscale API + agent heartbeat で取得。
  liveness は collector に依存しない(= 中央依存を作らない)。
- 各 agent は自ノードの tmux/エージェントセッションを HTTP で報告。任意ノードが
  他ノードの agent を直接叩ける P2P 構成。「オーナー/スレーブ」は UI 上の役割であって
  必須サーバーではない。

## 2. データの真実(source of truth)

- **各 agent がローカルに追記専用イベントログを持つ**(これが真実)。
  - イベント: session.open / session.close / exec / attach / detach など。各イベントに ULID。
- collector はそれを集めるだけの **ミラー**。
  - collector ダウン中 → agent はローカルに溜め続け、復帰時にリプレイ → **欠損なし・停止なし**。
- 初手は collector 単一 + ローカルバッファ。HA が要れば後で 2 台冪等 fan-out に拡張可能
  (イベント ULID で冪等性が担保されるので拡張は非破壊)。

## 3. セキュリティ(exec が主役なので最重要)

- **転送暗号化は Tailscale (WireGuard) に委譲**。自前 TLS なし。
- **リモート peer の認可は `tailscale whois` で identity を取得 → 自前ポリシーで判定**。
  ソケットの実 RemoteAddr を tailscaled に問い合わせるためなりすまし不可。
- **同一アカウント所有デバイスの自動信頼**: peer の login が agent 自身の tailnet
  login と一致すれば CapExec を付与。個人の複数マシンが per-node 設定なしで相互
  操作できる(他者の login は従来どおり policy 任せ)。
- **loopback はローカルトークンで認可**。`DataDir/agent.token`(0600、初回生成)を
  提示した呼び出しのみ owner(CapExec)。同一マシンの別ローカルユーザーによる乗っ取りを防ぐ。
  クライアントはこのトークンを **loopback 宛にしか送らない**(リモートへ漏れない)。
- **bind は最小面**。`:port` 指定時は loopback + 自ノードの Tailscale IP のみに bind し、
  LAN 全体には晒さない。明示 host:port 指定時はそれを尊重。
- **attach(閲覧)と exec(実行)を権限分離**。CapExec ⊇ CapAttach。ノードごとに
  「誰が exec 可能か」をポリシー化。
- **exec / 認可拒否(authz.deny)は全件監査ログ**(who / node / session / command / 結果)
  → ローカル + collector。
- 認可は interface で抽象化(today: Tailscale ACL、future: 独自コントロールプレーン/SaaS)。

### keychain に関する重要な制約

- セキュリティの回避ではない。自分のマシンに、自分が正規にログイン済みの状態で
  リモートからアクセスするための実務的な工夫。
- macOS の login keychain は GUI ログイン時にアンロックされる。素の SSH の非 GUI
  セッションはそこへアクセスできないため、GUI 起動で認証情報を keychain に持つ
  種類のツール全般(特定ツールに限らない)がリモートからは正しく動かないことがある。
- 対応: **GUI ログインセッション内で既に動いている tmux サーバに attach する**
  (正規にアンロック済みの環境をそのまま使う)。
- したがって **agent は LaunchAgent(GUI セッション内、Aqua)で起動する**。
  LaunchDaemon(root/ログイン前)は keychain も tmux サーバも見えず無効。← productize 時の必須注意。

## 4. セッションのライフサイクル

「1 terminal window ↔ 1 ノードの 1 tmux session」を束縛(binding)とする。
**kill のトリガーは明示的な意思表示のみ。lease 切れでは殺さない。**

- **kill する(明示的な意思)**
  - ctrl+c 連打(例: 400ms 以内に 3 回)→ clean exit + `tmux kill-session`
  - 窓を閉じる / macker quit(クライアントに SIGTERM)→ release RPC → kill
- **生かしたまま離脱する(明示的な意思)**
  - ctrl+j 連打(例: 300ms 以内に 3 回)→ クライアントだけ終了、session は生存
    (ephemeral でも kill しない)。再 attach で続きから戻れる。
- **生かす(意思表示なし = 事故扱い)**
  - sleep / 蓋閉じ / ネット瞬断 / クライアントのクラッシュ → session 生存・再 attach 可
- kill は **クライアントが agent に明示 release RPC を送ったときだけ**発火。
  heartbeat 途切れ + release 無し → `detached (orphaned)` だが生存。`macker ls` で可視化、
  `macker kill` で手動掃除。
- 逃げ道フラグ: `--keep`(長時間 AI タスクを裏で生かす) など。

### ctrl+c / ctrl+j 連打の実装メモ

- macker クライアントが PTY master として入力を中継。
- ctrl+c / ctrl+j とも**即座にリモートへ通しつつ回数カウント**。閾値超えで exit 発火。
- 単発 ctrl+c はリモートのプロセスに通常どおり効く(エディタ等を壊さない)。
  単発の ctrl+j も同様(改行と等価)。
- ctrl+c 連打は intent=close(ephemeral なら kill)、ctrl+j 連打は intent=detach
  (ephemeral でも kill せず lease 解放のみ)。
- **回数カウントは「1 read につき各 mash 最大 1 ヒット」**。
  人間の連打は OS から別々の read として届くのに対し、ペースト等で複数 LF が
  1 read に乗ってくる場合は 1 ヒットしか積まないので、ペーストで誤発火しない。
- ctrl+j (= LF) の detach はさらに 2 段の heuristic で paste / キーリピートを弾く:
  - **paste-shape filter**: 1 read 内に LF が 2 つ以上 → paste 扱いで 0 hit。
  - **inter-read min gap (デフォルト 50ms)**: 直前 hit から 50ms 未満で来た read
    は OS キーリピート (30-50ms/repeat) や paste の分割到着扱いで 0 hit。
    人間の連打 (80ms+) は通る。
  - 結果として detach が成立するのは **「各押下が 50ms 以上空く」かつ
    「3 連の総時間が 300ms 以内」** な領域。実質的に 50ms ≤ 押下間隔 ≤ 150ms
    の窓。自然な早打ち (80-100ms 間隔) は通り、長押しキーリピートやペーストは
    通らない。
- intent != natural のときは **SIGTERM → 短い待機 → SIGKILL** の段階的終了で
  ローカルクライアントを止め、ssh/sshd 側に clean な session close をさせる
  (リモートに `destroy-unattached` 等が設定されていても誤爆しにくくなる)。

## 5. CLI(MVP)

```
macker ls                    # ノード一覧 + 各ノードの session 一覧(オンライン/orphaned 込み)
macker attach <node>[:sess]  # ssh + tmux attach をラップ(束縛を確立)
macker exec <node> -- <cmd>  # 認可付きリモート実行(監査ログ)
macker grid n1 n2 n3 n4      # 各 node に attach した窓をグリッド配置
macker kill <node>:<sess>    # orphaned 等の手動掃除
macker agent                 # 常駐デーモン(LaunchAgent)。自ノードの session を報告
```

- グリッドを閉じる = 各窓が release → それぞれ kill。mac を sleep = 全 session 生存。
- 分割表示そのものは tmux/zellij/Ghostty に委ね、macker は「どの session に attach するか」の
  オーケストレーションに徹する。

## 6. 技術スタック(候補)

- 言語: **Go**(単一静的バイナリで CLI + daemon、Tailscale 純正が Go、tsnet 利用可、
  PTY/exec ライブラリが揃う)。
- agent ↔ client: tailnet 内 HTTP/gRPC(プロトコルはバージョニング)。
- ローカルログ: 追記専用(SQLite or 追記ファイル)。collector も同形式のミラー。
- session 識別: tmux ペインのプロセスツリーを覗き「これは Claude Code」等を判定して
  ジェネリックな tmux 一覧より一段リッチに表示。

## 7. 確定した判断

- collector: 単一 + ローカルバッファ。
- session 既定挙動: 明示 release のみで kill(sleep は生存)。ctrl+c 連打で離脱。
- exec はスコープに含む(本ツールの価値の核)。
- 自分専用で始め、最初からサービス化可能な構造で作る。

## 8. 実装済み(旧 未決項目)

- **agent ↔ client プロトコル**: HTTP/JSON(`/v1`)。認可スキーマは §3 の通り
  (loopbackトークン + whois + CapAttach/CapExec)。
- **session 識別**: tmux ペインの `pane_current_command`/`pane_title` を見て
  Claude セッションを検出(`internal/session`)。
- **グリッド**: 既定は tmux のタイル分割(`macker grid`、`--layout` 選択可、
  ペインタイトル=ターゲット)。`--mode windows` で macOS のネイティブ端末
  (Ghostty/iTerm/Terminal)に別ウィンドウ展開(実験的、未対応端末は tmux へフォールバック)。
- **マルチテナント**: tenant = tailnet。config に `contexts`(プロファイル)を持ち、
  `--context`/`MACKER_CONTEXT`/`current_context` で選択。非デフォルトコンテキストは
  state ディレクトリを分離。イベントは tenant でタグ付けし、collector は
  `<tenant>/<node>.jsonl` に分けて保存。
- **collector ミラーリング**: 各ノードの shipper がローカルログを cursor 追跡で
  collector へ転送。collector ダウン中はローカルにバッファし復帰時にリプレイ
  (ULID 高水位で冪等)。`macker collector` で起動。

### 認可とテナント分離の既知の制限(意図的受容)

- **collector のテナント境界はフラット**。`/v1/events` は CapExec(owners/exec_allow/
  loopback)に限定済みだが、ポリシーは collector インスタンス単位でテナント別ではない。
  CapExec を持つプリンシパルは `?tenant=` で他テナントも照会できる。単一オペレータ運用は
  許容範囲。真のマルチテナント分離(per-tenant ポリシー)は次フェーズ。
- **collect はシッパー信頼モデル**。`/v1/collect` は CapAttach の認証済みピアを信頼し、
  イベントの Node/Tenant と送信元 identity を照合しない(macker ノード名と Tailscale
  ノード名が一致しないため厳密照合は別途設計が要る)。偽イベント投入は将来の検討事項。
- collector の認可拒否は collector ノードのローカル監査ログ(`collector.events.jsonl`)に記録する。

## 9. lease とライフサイクル状態(§4 の運用面)

- attach 中は client が agent に lease を heartbeat 更新。
- `macker ls` のセッション状態:
  - **attached**: 有効な lease(または素の tmux クライアント)あり。
  - **orphaned**: ephemeral セッションで、クリーンな終了なしに holder が消えた
    (sleep/クラッシュ → lease 失効)。`macker kill` で掃除。
  - **detached**: 生存しているが誰も attach していない(クリーン detach / 外部生成 / --keep)。
