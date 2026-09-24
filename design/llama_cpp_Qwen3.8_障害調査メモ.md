# llama.cpp・Qwen3.8 応答失敗の調査メモ

更新日: 2026-09-23

## 目的

Discord Bot Ziiで、検索ツールを使った回答が途中で失敗し、Discordへ返らなくなる事象について、これまでの切り分けと次回の確認事項をまとめる。

## 現象

- 挨拶などの短い質問は応答できる。
- 検索が必要な質問では、LLM呼び出しと検索ツール呼び出しが成功した後、LLM呼び出しが失敗し、最終回答がDiscordへ返らないことがある。
- LLMサーバー側には、Jinjaテンプレートエラーが複数回発生した。
- あるサーバーでは、最初に一度だけ応答した後、約90秒から100秒かかるLLM失敗が続いた。

## 確認したログ

### llama.cpp側

該当時間帯の2つの生成タスクで、どちらも生成トークン数が4096に達していた。

| task | prompt評価 | 生成 | 合計時間 | 生成速度 |
|---|---:|---:|---:|---:|
| 8884 | 4233 tokens / 約5.74秒 | 4096 tokens / 約92.07秒 | 約97.81秒 | 約44.48 tokens/秒 |
| 10428 | 4261 tokens / 約5.75秒 | 4096 tokens / 約83.97秒 | 約89.72秒 | 約48.77 tokens/秒 |

この所要時間はZii側のLLM失敗ログ（約98秒、約90秒）とよく一致する。出力上限に達した可能性が高い。ただし、当時の該当Chat Completions応答に含まれる`finish_reason`と`usage.completion_tokens`は未確認なので、4096到達が失敗の直接原因とまでは確定していない。

llama.cppログの`truncated=0`は、少なくともプロンプトのコンテキスト切り詰めが起きていないことを示すもので、生成上限に達していないことの証明ではない。

### 以前に確認したテンプレートエラー

- `No user query found in messages.`
  - `/v1/chat/completions/input_tokens`へ`role: tool`だけを送るとHTTP 500を再現した。
  - ツール結果だけのトークン数確認リクエストに、ユーザーメッセージのマーカーを加える修正を行った。
- `System message must be at the beginning.`
  - 出力長上限に達した場合の再試行で、既存のsystemメッセージより前に追加systemメッセージを挿入する構造が原因と判断した。
  - 再試行指示を先頭のsystemメッセージへ統合する修正を行った。

これらの修正後、ツール呼び出し自体は成功するケースが確認されている。しかし、長い検索回答の最終生成がタイムアウトまたは失敗する問題は残っている可能性がある。

## 実施済みのZii変更

- 検索ツール呼び出し回数に上限を設定した。
  - 既定値: 合計3回
  - `search_web`: 1回
  - `fetch_page`: 2回
- `/input_tokens`へtoolメッセージだけを送らないようにした。
- 再試行時にsystemメッセージの順序を壊さないようにした。
- 上記の変更はGitHubへpush済み。
  - `15aacab Limit default search tool calls`
  - `eaf018a Fix truncated retry system prompt ordering`

検索回数制限は過剰な検索の抑制にはなるが、個々の取得ページ本文が非常に長い場合の入力・生成負荷を必ずしも解消しない。

## LLMサーバー環境

ユーザーから共有されたバージョンとモデル:

```text
llama-server version: 0.4.0-dev (build 10919, commit d3146f2b5)
model: Qwen3.8-27B-Q6_K_L.gguf
```

現在の起動設定:

```bash
--model "${MODEL_PATH}" \
--alias qwen \
--host 127.0.0.1 \
--port 8080 \
--load-mode dio \
--ctx-size 65536 \
--batch-size 512 \
--ubatch-size 128 \
--n-gpu-layers 999 \
--device Vulkan1 \
--flash-attn on \
--jinja \
--fit off \
--cache-type-k q8_0 \
--cache-type-v q8_0 \
--spec-type draft-mtp \
--spec-draft-n-max 3 \
--spec-draft-p-min 0.70 \
--spec-draft-ngl all
```

`--help`出力でこのビルドが`--reasoning [on|off|auto]`を受け付けることは確認済み。推論を無効化すると取得HTMLをそのまま出すような回答になったため、今後は推論を有効にしたまま調査する。サーバー設定は現状維持とし、`--reasoning off`は採用しない。

## 現在の見立て

有力な仮説は、Qwen3.8が長い検索結果やページ本文を受けて推論を続け、応答が4096出力トークンに到達すること。最初の生成が上限に達した後、再試行も同じ上限を消費して失敗した可能性がある。

これはログとの整合性が高いが、再現時の応答メタデータが不足しており、確定診断ではない。`reasoning_content`が出力に含まれていた例はあるが、それだけで全4096トークンが推論に費やされたとは言えない。

## 次回の切り分け手順

1. **1回の失敗リクエストを追跡する**
   - Ziiログの同じ`request_id`について、LLM要求ごとの開始・完了・失敗時刻を記録する。
   - llama.cppの同時間帯の`task`と対応づける。

2. **LLM応答メタデータを確認する**
   - 各LLM応答の`choices[0].finish_reason`と`usage.completion_tokens`を確認する。
   - `finish_reason=length`、またはcompletionが4096付近なら出力上限到達を確認できる。
   - 可能なら`reasoning_content`と通常の`content`の各長さも記録する。全文や秘密情報は通常ログへ出さない。

3. **入力コンテキストの肥大化を調べる**
   - 各tool結果の文字数または概算トークン数を、ツール名・リクエストIDとともに記録する。
   - `fetch_page`が返す抽出本文のサイズと、ページ取得後の会話全体のサイズを見る。
   - 長大なHTMLやナビゲーション、重複ページ内容がLLMへ渡っていないか確認する。

4. **推論を維持したまま入力を制御する**
   - `fetch_page`の抽出本文に上限を設ける案を評価する。
   - 上限超過時はページ全体を渡さず、関連箇所を抽出または短く要約してからLLMへ渡す。
   - 検索回数上限とページ本文サイズ上限を別々に調整し、どちらが効果を出したか分かるようにする。

5. **再試行の出力上限と条件を確認する**
   - 再試行の`max_tokens`が初回と同じ4096か確認する。
   - 再試行時に元の長いtool結果をすべて再投入していないか確認する。
   - 長いページ結果を含む同じコンテキストを繰り返し送るだけなら、再試行を短い回答指示にするか、再試行条件を見直す。

### 診断ログで最低限ほしい項目

```text
request_id
LLM呼び出し回数（初回/再試行）
max_tokens
finish_reason
usage.completion_tokens
tool名ごとの結果サイズ（文字数または概算token数）
LLM呼び出しのduration_ms
```

## 次回の作業候補

まずログまたは再現応答から`finish_reason`と`usage.completion_tokens`を採取し、出力上限到達を確定する。その後、`fetch_page`の返却サイズ制限と、再試行時に渡すコンテキスト量を見直す。推論モードは有効のままにする。

## 次回の確認手順（2026-09-24）

目的は、検索を伴う1リクエストについて、最終回答が失敗する直接原因と、初回生成・再試行それぞれの状態を確認すること。調査中はモデル、llama.cppの起動設定、推論モードを変更しない。

### 1. 再現前の準備

1. Ziiとllama.cppのログで、同じ時刻を比較できるよう時刻・タイムゾーンを確認する。
2. 調査対象の1リクエストを識別する`request_id`を記録できるようにする。llama.cppログでは生成タスクの`task`も記録する。
3. 現在の設定値を記録する。特に`LLM_MAX_TOKENS`（未設定なら既定値4096）、`LLM_TIMEOUT`（未設定なら既定値180秒）、モデルID、llama.cppのバージョンと起動オプションを控える。値は調査中に変更しない。
4. **応答メタデータの採取方法を先に用意する。** 現行Ziiは`finish_reason`をエラー判定に使う一方、`usage`を保持・ログ出力しない。そのため既存のZiiログだけでは`usage.completion_tokens`を確認できない。既存の安全なHTTPトレース手段がなければ、調査用に応答の`finish_reason`、`usage.prompt_tokens`、`usage.completion_tokens`だけを一時的に記録する方法を用意する。メッセージ本文、tool引数、ページ本文、APIキーは記録しない。変更を加える場合は計測だけに限定し、調査後に戻す。

### 2. 失敗を1回再現する

1. 短い挨拶ではなく、これまで失敗したものに近い検索質問を1つ送る。調査中に同時リクエストを流さない。
2. Ziiログから`request_id`、要求開始・終了時刻、検索ツール名と呼び出し回数、各LLM呼び出しの所要時間を保存する。
3. llama.cppログから同じ時間帯の生成`task`、prompt評価トークン数、生成トークン数、終了状態、所要時間を保存する。
4. 応答計測から、初回と再試行のそれぞれについて、`max_tokens`、`finish_reason`、`usage.prompt_tokens`、`usage.completion_tokens`、所要時間を対応づける。1回しかLLM呼び出しがなければ、その事実も記録する。
5. ツール結果の内容自体は保存せず、`search_web`、`fetch_page`などツール名ごとに返却文字数と概算または実測トークン数だけを記録する。ページ結果が予算超過で短縮されたかも記録する。

### 3. 出力上限到達を判定する

- 初回または再試行の`finish_reason`が`length`なら、その呼び出しは生成上限で終了したと判定する。
- `finish_reason=length`に加えて`usage.completion_tokens`が設定上限（通常4096）付近なら、今回の最終失敗が出力上限に達した生成と再試行で起きた、という仮説を強く支持する。
- `finish_reason=stop`でcompletion token数も上限から離れている場合、出力上限仮説は支持されない。該当するHTTPエラー、キャンセル、タイムアウトなど別の終了理由を追う。
- 応答メタデータが採れず、llama.cppログに生成トークン数だけがある場合は「上限到達の疑い」にとどめ、確定扱いにしない。
- llama.cppの`truncated=0`はコンテキスト入力の切り詰めに関する値として扱い、出力上限未到達の根拠にはしない。

### 4. 再試行の入力とページ結果の影響を判定する

1. 初回と再試行の`max_tokens`を比較する。現行実装では両方とも同じLLMクライアント設定値を使う想定なので、差があれば実際の送信要求を優先して記録する。
2. 初回と再試行のprompt token数を比較する。再試行ではツールを無効にして短い回答指示を加えるが、会話中の既存メッセージとツール結果は残る。入力がほぼ減っていない場合は、その点を記録する。
3. `fetch_page`結果の制限が実際に適用されたか確認する。現行実装には1ページあたり最大8Kトークン、ツール結果全体で最大24Kトークンの予算がある。上限値を新たに変更する前に、対象リクエストで短縮が発生したか、各ページ結果と合計入力がどの程度だったかを確認する。
4. 生成上限到達が確認され、かつ長いtool結果が再試行にも残っている場合は、まず短縮再試行の入力削減が必要かを次の変更候補として評価する。ページサイズ予算の変更は、実測で現行上限が不十分と分かった場合に限って別途検討する。

### 5. 記録して調査を終了する

次の形式で1件分の結果を残し、診断が確定したか、追加計測が必要かを明記する。

```text
request_id:
llama.cpp task IDs:
result: success / failed
LLM call 1: duration_ms, max_tokens, finish_reason, prompt_tokens, completion_tokens
LLM retry: none / duration_ms, max_tokens, finish_reason, prompt_tokens, completion_tokens
tool result sizes: tool name, characters, tokens, truncated yes/no
llama.cpp: prompt tokens, generated tokens, duration_ms, end state
diagnosis: confirmed / supported / not supported / inconclusive
next evidence needed:
```

診断が「確定」または「支持されない」になったら、その証拠に基づいて次の修正対象を決める。メタデータが不足している場合は、設定を変えて再試行する前に計測方法を補う。
