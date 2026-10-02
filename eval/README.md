# Zii LLM Evaluation Harness

固定の質問セットをZiiの本番 `ToolLoop` に渡し、機械的な異常・応答時間・token使用量を測るCLIです。モデル変更、推論エンジン変更、Zii修正前後の比較に使います。

## 構成

| 場所 | 役割 |
|---|---|
| `testcases/basic.yaml` | 通常会話、JSON、日本語、固定の会話履歴 |
| `testcases/tools.yaml` | 単一Tool、3段階Tool、実Search MCP |
| `testcases/reasoning.yaml` | 計算、推論、長文からの抽出 |
| `testcases/edge_cases.yaml` | 曖昧な質問、Unicode、Tool失敗、検索結果なし |
| `profiles/example.yaml` | モデル、endpoint、生成設定、実行環境の記録 |
| `../cmd/zii-eval` | check / run / compare コマンド |
| `../internal/eval/runner.go` | 本番ToolLoopへの接続と反復実行 |
| `../internal/eval/validator.go` | 異常・期待結果の判定 |
| `../internal/eval/metrics.go` | 計測と集計 |
| `../internal/eval/report.go`, `compare.go` | 結果保存と修正前後・モデル比較 |
| `../reports/` | JSON・CSV・Markdown結果（Git管理外） |

初期セットは23件です。`fixture`では21件を実行、`live`専用2件をSKIPします。`live`では通常質問と実検索2件を実行し、架空のURL・データを使う6件をSKIPします。各caseは独立し、履歴やfixtureの消費状態は次のcase・反復へ持ち越しません。

## 最初の実行

リポジトリ直下で実行します。Go 1.25以上が必要です。Discordの起動・Bot Token・SQLiteの準備は不要です。

```bash
cp eval/profiles/example.yaml eval/profiles/local.yaml
```

`local.yaml`の`model_profile_id`をZiiの設定で有効なプロファイルIDに変更してください。`model_profile_config`は評価YAMLからの相対パスで、選択したプロファイルのmodel・endpoint・Capability・既定生成設定を本番と共用します。解決した設定もJSONに記録します。単独の診断設定を使う場合は参照2項目を省略し、`model`と`base_url`を指定できます。この場合はToolsとreasoningを有効、複数Tool Callを無効とする汎用設定になります。**`base_url`に`/v1`は付けません。** `environment`にはモデルファイル・量子化・GPU・サーバーのバージョンや起動設定を書きます。これは実行時にサーバーから自動取得した情報ではありません。

```bash
go build -o bin/zii-eval ./cmd/zii-eval
./bin/zii-eval check --profile eval/profiles/local.yaml

# まずTool不要の1問で接続とレポートを確認
./bin/zii-eval run \
  --profile eval/profiles/local.yaml \
  --id arithmetic_exact \
  --out reports/smoke

# 質問セットを1周（初回の動作確認用）
./bin/zii-eval run \
  --profile eval/profiles/local.yaml \
  --out reports/baseline
```

`check`はYAML検査だけを行い、LLM/MCPへ接続しません。`run`は実LLMへ問い合わせます。`fixture`でもLLM自体は実物です。

## 比較可能な測定

```bash
./bin/zii-eval run \
  --profile eval/profiles/local.yaml \
  --warmup 1 --runs 3 \
  --label before --out reports/before

# Zii・モデル・設定など、比較したい対象を変更する。
# コードを変更した場合は同じコマンドで再ビルドする。
go build -o bin/zii-eval ./cmd/zii-eval

./bin/zii-eval run \
  --profile eval/profiles/local.yaml \
  --warmup 1 --runs 3 \
  --label after --out reports/after

./bin/zii-eval compare \
  --before reports/before/report.json \
  --after reports/after/report.json \
  --out reports/comparison
```

warmupは**選択したセット全体を指定回数実行**し、測定集計から除外します。その後、同じ順序で`runs`回実行します。上の例ではfixture対象21問 × 4周 = 84問を実LLMへ送ります。1問数分かかる環境では、最初は`--warmup 0 --runs 1`、または`--id` / `--category`で絞ってください。

比較時には質問・期待値・fixtureのSHA-256、Toolモード、回数、timeout、Tool上限、token計数方式、Tool定義、case選択条件の一致を必須とします。異なるセットや途中終了した結果を同じ条件の比較として扱いません。モデル・生成設定の差は比較レポートに列挙します。System Promptの変更も表示します。意図した差以外を揃えてください。

温度0や同じseedでも、推論エンジン・GPU・サンプリング実装の差による完全な決定性は保証されません。モデルロード直後か、prefix/KVキャッシュが温まっているか、並行リクエストがあるかでも時間は変化します。本CLIは逐次実行です。サーバー側のキャッシュ状態を自動リセットする処理はありません。比較中は他のLLM利用を避け、`environment.cache_policy`に条件を記録してください。

## 固定Tool応答と実Search MCP

| `tool_mode` | 用途 | 動作 |
|---|---|---|
| `fixture` | 再現性を重視した回帰・モデル比較 | テストcaseの固定応答をTool名別・呼出順に返す。検索結果やネットワークの変動を除く |
| `live` | 実際の検索サービスを含む疎通・運用確認 | Ziiの実MCPクライアントで接続し、`tools/list`の定義で呼び出す |

実MCPの確認はプロファイルを複製し、`tool_mode: live`に変更して実行します。

```bash
./bin/zii-eval run \
  --profile eval/profiles/local-live.yaml \
  --category live-tool \
  --out reports/live
```

fixtureはSearch MCP main `8a4bedb39dff0d77bff2b02c7e9e1785fe9eac62`の3つの入力型に対応する固定スキーマを使用します。検索の意味的妥当性や実際のURL到達性、検索結果の正しさは模擬しません。fixture不足・消費超過・`match`不一致はFAILです。LLMの質問表現が変わっても同じ証拠を返せるよう、queryの厳密一致は通常要求しません。

liveでは検索キャッシュやインデックスの状態が変化し、`fetch_page`がSearch MCP側の保存処理を実行する場合があります。厳密な修正前後比較にはfixtureを使い、liveの性能値には検索サービスの時間も含まれると解釈してください。

## 判定と終了コード

| 状態 | 意味 |
|---|---|
| PASS | 最終回答と明示した期待値が正常、途中の異常なし |
| WARN | 最終回答は期待値を満たすが、途中にlength・markup・HTTP再試行などの異常あり |
| FAIL | 最終回答なし、実行エラー、期待値違反、fixture不備など |
| SKIP | 選択Toolモードとcaseの対象モードが異なる |

`stop`＋空白/null/空content、reasoningのみ、finish_reason不正、Tool Call構造・引数・スキーマ不正、未知のTool、Tool禁止後の呼出、重複ID/同一Tool引数の反復、上限超過、markup漏れ、timeout、HTTP失敗、MCP失敗などを記録します。`parallel_tool_calls` Capabilityが有効なら、1応答に複数のstructured Tool Callを含む正常な応答も許容します。無効な場合は異常扱いします。**空contentでも有効な`tool_calls`応答なら正常**です。

既存Ziiのlength/markup/通信再試行はそのまま通します。評価基盤独自の空回答retryやfinal指示、grammarの追加は行いません。再試行前の異常とtoken使用量もtraceに残します。`allow_tool_errors: true`は意図した`isError`応答だけを許容し、fixture不備や通信失敗は許容しません。

- `0`: 正常。compareでは状態悪化なし。
- `1`: FAILまたはWARNあり。`--fail-on-warn=false`ではWARNを許容。compareではPASS→WARNなどの悪化あり。
- `2`: 設定、初期接続、保存、比較条件のエラー。
- `130`: Ctrl+C/SIGTERMで中断。完了したsampleと中断sampleを保存。

終了コード1でもレポートは保存されます。途中結果は各sampleの完了時に`samples.jsonl`へ追記します。強制終了しても記録済み行は残ります。通常の中断時には最終JSON/CSV/Markdownも保存します。既存出力フォルダへの上書きは拒否します。

## 計測値の読み方

| 指標 | 意味・制約 |
|---|---|
| `total_ms` | 1問のToolLoop全体。LLM、Tool、token計数、retry待ち時間を含む |
| `llm_http_ms` | 全Chat Completions通信試行の時間合計。prefill・生成・通信時間を含む |
| `token_count_http_ms` | Tool budget計算のためのtoken計数HTTP時間 |
| `tool_ms` | MCPクライアント呼出時間の合計。fixtureでは固定応答処理時間 |
| `prompt_tokens`, `completion_tokens` | 全LLM試行のAPI usage合計。1試行でも不明なら合計はnull |
| `reasoning_tokens` | `usage.completion_tokens_details.reasoning_tokens`。ない場合はnull。completionに重複加算しない |
| `completion_tokens_per_llm_second` | completion tokens / LLM HTTP秒数。純粋なdecode速度ではない |
| `server_decode_tokens_per_second` | サーバーが全応答で返す`timings.predicted_n`合計 / `timings.predicted_ms`合計。取得できる場合だけ記録 |
| `requested_tool_calls` | モデルが返したTool Call数。拒否された呼出も含む |
| `tool_attempts` | 実際にToolクライアントへ到達した呼出回数。再試行も含む |
| `retry_count` | LLM HTTP retry + token計数HTTP retry + 最終回答retry + Toolクライアントretry |

MCP SDKやHTTP transport内部の透過的な再送は`retry_count`の対象外です。CLI起動・モデルロード・MCP初期接続の時間も`total_ms`には含めません。Tool timeout等の内訳は`trace`で確認できます。

欠測値を0として扱いません。CSVでは欠測セルは空欄です。平均・中央値・min/max・nearest-rank P95を出しますが、少数サンプルのP95には強い意味を持たせないでください。比較の時間差は、**両方でPASS/WARNだった同じcase・反復番号**だけで計算します。失敗が増えて見かけ上速くなった結果を高速化として集計しません。異なるモデルはtokenizerが違うため、tokens/secだけで比較しないでください。

Ziiの既存token計数は`/v1/chat/completions/input_tokens`を要求します。未対応エンジンでは`token_counter: estimate`を**明示**できます。これはJSONのUnicode文字数をTool budgetの近似に用いるだけで、正確なtoken数ではありません。usageや性能指標には流用せず、server方式と同条件の比較もしません。自動フォールバックはありません。

## テストケースの追加

YAMLの不明キー、重複ID、不正な正規表現、複数documentはエラーにします。ファイル追加・質問追加にGoコードの変更は不要です。

```yaml
version: 1
cases:
  - id: custom_question
    category: basic
    tools: none
    timeout: 120s
    prompt: "2 + 3 の答えを半角数字だけで答えてください。"
    expect:
      equals: "5"
      max_tool_calls: 0
      max_total_ms: 60000
```

`expect`は任意です。常に非空の正常な最終回答を要求します。`equals`、`contains`（全要素）、`pattern`（Go正規表現）、`min_tool_calls`/`max_tool_calls`、`required_tools`、`forbidden_tools`、`allow_tool_errors`、`max_total_ms`を指定できます。`required_tools`は実際のToolクライアント呼出を要求します。

通常の応答で検索を避けるかも検査したい場合は`tools: auto`＋`max_tool_calls: 0`にします。`tools: none`は最初からTool定義を渡しません。固定の会話履歴は`history`にuser/assistantの完全なペアで指定できます。`modes: [fixture]`または`[live]`で対象モードを限定します。省略時は両方対象です。

## 保存情報と対象範囲

通常は本文を保存せず、finish reason、空content判定、reasoning有無、再送されたreasoningメッセージ数、Tool名、token使用量などを保存します。`reasoning_content`本文や生HTTP body、APIキーをレポートへ保存しません。最終回答の目視レビューが必要な場合だけ`--save-answers`を指定してください。その場合の`content`にモデル自身が思考文を混入したケースは、最終回答として保存される可能性があります。

固定の質問・fixtureとプロファイルは再現条件としてJSONへ保存されます。これらの設定には秘密情報を書かず、認証キーは`api_key_env`で環境変数を指定してください。自動比較できるのは機械的異常と明示した期待値です。一般的な正確さ・有用性・自然な日本語の評価を網羅するものではありません。

本基盤はmain `064ccb1877cddd6578d0b77b11cab82eb1073177`の複数モデル対応・reasoning再送・Tool上限後の最終回答処理に接続しています。本番ToolLoopやモデル名分岐は変更しません。Tool上限後の通常の最終回答生成は再試行数に含めず、length/markupからの回復だけを最終回答再試行として数えます。source欄はバイナリのGo build情報と、実行ディレクトリのGit revision/dirty/source hashを別々に記録します。ソース変更後は再ビルドしてください。

Discord Gateway・送信権限・返信分割・Queue・SQLite保存・履歴の選別は今回の測定対象外です。固定履歴を直接ToolLoopへ渡します。ストリーミングTTFT、GPU使用率、並列負荷試験、自動LLM採点は未実装です。

## 開発時の検証

```bash
go test ./...
go vet ./...
go test -race ./internal/eval ./cmd/zii-eval
go build -o bin/zii-eval ./cmd/zii-eval
```

自動テストは模擬LLM HTTP応答とローカルMCPサーバーを使います。これは実モデルの評価成績ではありません。空回答、Tool異常、length/markup/HTTP retry、複数Toolターン、timeout、中断、usage欠測、reasoning/認証キー非保存、YAML検査、CLI出力、比較条件・成功ペアの集計を検証します。
