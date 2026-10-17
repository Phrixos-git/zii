# Zii

[English](README.md)

ZiiはGoで実装したDiscord Botです。ユーザーごとの会話履歴をSQLiteへ保存し、OpenAI互換LLMに回答を生成させます。必要に応じてSearch MCPが提供するToolを使用します。

## Zii Ver1.2の自己紹介とリリース情報

ZiiはDiscord上で動作するAIアシスタントです。選択したLLMを基盤モデルとして使用します。Ver1.2では、会話・検索・モデル選択・評価の機能に加え、Zii自身の説明、提供機能、リリース情報を回答するための仕組みを追加しました。

現在の機能は、質問への回答、文章作成、情報整理、設定された上限と有効期限の範囲での会話履歴の利用、Zii自身とリリース情報の説明です。Search MCPは、選択したプロファイルで`tools`が有効になり、必要なToolが登録されている場合に使用できます。起動時にはSearch MCPへの接続が必要です。設定上は利用可能でも、通信障害で検索に失敗する場合があります。最近の会話履歴には上限があり、永続的なUser Memoryではありません。

情報の管理元は[`internal/identity/manifest.yaml`](internal/identity/manifest.yaml)です。Ver1.0とVer1.2は実装済みの`released`、Ver1.5（DeepSeek Harness / Agent）、Ver2.0（User Memory / Personalized Context）、Ver2.5（Memory最適化）、Ver3.0（視覚的な出力 / 画像生成）は予定の`planned`として定義しています。予定のリリース日は未定です。manifestは、そのバイナリが持つ機能を表します。ブランチのマージやデプロイの状況を表すものではありません。

リリース情報を更新する場合はmanifestを編集し、`current_version`を一覧にある`released`のバージョンに設定して、`cmd/zii`と`cmd/zii-eval`の両方を再ビルドします。manifestはバイナリへ埋め込むため、実行時の追加ファイルや環境変数は不要です。実行中の再読み込みは行いません。未知の項目、不正なYAML、複数のYAML文書、必須項目の空欄、バージョン・機能ID・必要Toolの重複、不明な状態、現在版の欠落や`planned`指定があると起動に失敗します。認証情報、接続先、DBパスなどの非公開設定はmanifestへ記載しないでください。

Discordと評価CLIは`orchestrator.BuildSystemPrompt`を共用します。検索・セキュリティの方針に、manifestと公開可能な実行時情報を組み合わせます。実行時情報は、設定したAPIのモデル識別子と検索機能の利用可否です。APIの識別子だけでは実際のモデルの重みや開発元を確認できません。

評価CLIは組み立てたPromptのハッシュと、各サンプルで実際に使ったPromptのハッシュを記録します。Toolを無効にするケースも対象です。そのためmanifestを変更すると、評価の再現条件を示すハッシュも変わります。内部の呼び出し元が`ServiceConfig.SystemPrompt`を指定した場合は、共通Promptを置き換えず、信頼できる追加指示として付与します。

自己紹介・リリース情報の評価ケースは[`eval/testcases/identity.yaml`](eval/testcases/identity.yaml)にあります。実装上の判断と検証結果は[`docs/ver1.2-implementation.md`](docs/ver1.2-implementation.md)を参照してください。

## 必要なもの

- ソースからのビルド・実行にはGo 1.25以上が必要です。
- DiscordアプリケーションとBot Tokenを用意します。Gateway Intentは`Guilds`、`Guild Messages`、`Direct Messages`を使用します。Ziiは特権のMessage Content Intentを要求しません。
- Botにはチャンネルの表示、メッセージ送信、スレッド内の送信、メッセージ履歴の閲覧権限を付与します。
- 選択したモデルを読み込んだOpenAI互換LLMサーバーが必要です。`POST /v1/chat/completions`と`POST /v1/chat/completions/input_tokens`に対応している必要があります。後者で使用中モデルとChat Templateに基づく履歴・Tool ResultのToken数を計算します。Botは概算への自動切替を行いません。接続先はモデルプロファイルで指定します。Tool Callingは、そのプロファイルの`tools`が有効な場合に使用します。
- Streamable HTTPで接続でき、`search_local`、`search_web`、`fetch_page`を提供するSearch MCPが必要です。既定の接続先は`http://127.0.0.1:8081/mcp`です。
- SQLite DBの保存先に、書き込み可能なディレクトリを用意します。DBパスは絶対パスで指定し、親ディレクトリをあらかじめ作成してください。

Ziiは起動時にSearch MCPへ接続し、Tool定義を取得します。接続できない場合や必須Toolを読み込めない場合は終了します。LLM Clientの設定も起動時に検証しますが、LLMサーバーへの通信は質問の処理時に行います。Discordで動作確認する前に、接続先とモデルの準備を確認してください。

## 設定

リポジトリ直下で非公開の環境設定ファイルを作成し、必要な値を編集します。

```sh
cp .env.example .env
chmod 600 .env
```

`.env`には次の必須項目を設定します。

```dotenv
DISCORD_BOT_TOKEN=your-discord-bot-token
BOT_DB_PATH=/absolute/path/to/zii/data/bot.db
LLM_MODEL=qwen
```

起動前にDBの保存先ディレクトリを作成します。

```sh
mkdir -p /absolute/path/to/zii/data
```

モデルID、LLMの接続先、Capability、対応する推論強度、生成設定の既定値は`config/model_profiles.yaml`で管理します。`LLM_MODEL`は`models:`配下のキーを選び、APIにはプロファイルの`model`をモデル名として送信します。モデルや接続先を変更する場合は、このファイルのプロファイルを追加・編集してください。

`.env`はGit管理外で、Zii自身は自動で読み込みません。後述のコマンドでは起動前に内容を環境変数として読み込みます。Bot Tokenを含むため、`.env`をコミットしたり共有したりしないでください。従来の`LLM_BASE_URL`は使用せず、選択したYAMLプロファイルの`endpoint`へ接続します。異なる接続先を`LLM_BASE_URL`へ設定している場合は警告を記録します。

`config/gpt-oss-20b.profile.example.yaml`はプロファイル追加の参考例で、自動では読み込みません。接続先はローカル環境の例なので、使用前に実際のサーバーと一致させてください。有効な設定ファイルには既に`gpt-oss-20b`があり、参考例の異なる既定値で上書きされることはありません。推論強度とCapabilityは、対象モデルと実行環境の対応状況を確認して設定します。

### モデルプロファイルとCapability

Ziiで選択できるプロファイルは`config/model_profiles.yaml`にあります。`models:`配下のIDをキーとし、APIのモデル名、接続先、6つのCapability、対応する推論強度、生成設定の既定値を定義します。

```yaml
models:
  qwen:
    model: qwen
    endpoint: http://127.0.0.1:8080
    capabilities:
      tools: true
      reasoning: true
      reasoning_content: true
      reasoning_effort: true
      thinking_budget: true
      parallel_tool_calls: true
    supported_reasoning_efforts:
      - low
      - medium
      - xhigh
    defaults:
      reasoning_effort: xhigh
      thinking_budget_tokens: 2048
```

同梱のQwenプロファイルは6つのCapabilityをすべて有効にしています。推論強度の対応値は`low`、`medium`、`xhigh`で、現在の既定値は`xhigh`です。これらはZiiが受け付ける設定の宣言であり、実際のサーバーの動作確認ではありません。対象モデルと実行環境が設定値に対応しているか確認してください。

| Capability | 有効にした場合の動作 |
| --- | --- |
| `tools` | Search MCPのTool定義を送信し、Tool Callingを使用します。無効の場合は定義を送らず、Toolを実行しません。 |
| `reasoning` | 個別のCapabilityも有効な推論設定を送信できます。 |
| `reasoning_content` | APIの`reasoning_content`を読み取り、ユーザー向けの回答本文と分けて保持します。 |
| `reasoning_effort` | 設定した推論強度を送信できます。値は`supported_reasoning_efforts`の一覧にあるものに限ります。 |
| `thinking_budget` | `thinking_budget_tokens`を送信できます。`reasoning_effort`とは別の設定です。 |
| `parallel_tool_calls` | Assistantの1応答に複数のTool Callを含めることを許可します。実行は既存の処理に従い、この設定で並列実行の仕組みを追加するわけではありません。 |

Capabilityは機能やRequest項目の使用可否を制御し、値そのものは指定しません。対応するCapabilityが無効な推論設定はAPIへ送信しません。`reasoning_effort`が有効な場合、`supported_reasoning_efforts`にない値は拒否します。Capabilityを含むプロファイルの項目は起動時に検証します。不正なYAML、未知の項目、設定の欠落、存在しない`LLM_MODEL`の指定は、自動補正せず起動エラーとします。

有効な設定ファイルには`qwen`、`gpt-oss-20b`、`ministral-3-14b-instruct-2512`を同梱しています。既存のプロファイルを使う場合は接続先を確認し、`LLM_MODEL`で選択します。別のモデルを追加する場合は`config/gpt-oss-20b.profile.example.yaml`を参考に、`models:`配下へ新しいキーで定義します。既存のキーを重複させないでください。Goコードの変更や再ビルドは不要です。

実際の接続先とAPIのモデル名を設定し、Capability、推論強度の対応値、既定値をモデルと実行環境に合わせます。`LLM_MODEL`を使用するプロファイルのキーに設定してZiiを再起動してください。別の設定ファイルを使う場合は`LLM_PROFILE_CONFIG`で指定します。相対パスはプロセスの作業ディレクトリを基準に解決します。

### 環境変数

前述の必須3項目を除き、`.env.example`に記載した既定値で使用できます。

| 変数 | 既定値 | 用途 |
| --- | --- | --- |
| `DISCORD_BOT_TOKEN` | 必須 | Discord Bot Token。 |
| `BOT_DB_PATH` | 必須 | SQLite DBの絶対パス。親ディレクトリは作成済みで、書き込み可能であること。 |
| `LLM_MODEL` | 必須 | YAMLの`models:`配下にあるプロファイルのキー。 |
| `LLM_PROFILE_CONFIG` | `config/model_profiles.yaml` | モデルプロファイルのYAML。相対パスは作業ディレクトリ基準。 |
| `LLM_TIMEOUT` | `180s` | LLMへの各HTTPリクエストのTimeout。 |
| `LLM_MAX_TOKENS` | `4096` | 通常のLLMリクエストの出力Token上限。 |
| `LLM_REASONING_EFFORT` | 空文字 | 推論強度の任意指定。対応するCapabilityが有効な場合だけ送信。 |
| `LLM_THINKING_BUDGET_TOKENS` | `0` | 推論用Token予算の任意指定。対応するCapabilityが有効な場合だけ送信。 |
| `LLM_MAX_CONCURRENCY` | `2` | LLMリクエストの最大同時実行数。 |
| `SEARCH_MCP_URL` | `http://127.0.0.1:8081/mcp` | Search MCPのStreamable HTTP接続先。 |
| `SEARCH_MCP_TIMEOUT` | `30s` | Search MCPリクエストのTimeout。 |
| `MCP_MAX_CONCURRENCY` | `4` | Search MCP呼び出しの最大同時実行数。 |
| `CONVERSATION_TTL` | `168h` | 最終受付から、この期間が経過すると新しいConversationを開始。保存済みデータは削除しない。 |
| `CONVERSATION_MAX_TURNS` | `5` | Contextへ入れる完了済みの過去のUser / Assistant往復数。指定範囲は1〜5。 |
| `CONVERSATION_MAX_TOKENS` | `8192` | 過去履歴だけに適用するToken予算。指定範囲は1〜8192。 |
| `TOOL_MAX_CALLS` | `3` | 1質問あたりのTool Call合計上限。 |
| `SEARCH_LOCAL_MAX_CALLS` | `2` | 1質問あたりの`search_local`上限。 |
| `SEARCH_WEB_MAX_CALLS` | `1` | 1質問あたりの`search_web`上限。 |
| `FETCH_PAGE_MAX_CALLS` | `2` | 1質問あたりの`fetch_page`上限。 |
| `REQUEST_QUEUE_SIZE` | `10` | 待機中のDiscord Requestの最大件数。 |
| `QUEUE_WAIT_TIMEOUT` | `180s` | Queueで待機できる最大時間。 |
| `REQUEST_TIMEOUT` | `300s` | Queue待機と回答の配信を含むRequest全体のTimeout。 |
| `APP_TIMEZONE` | `Asia/Tokyo` | 相対日付・時刻の回答で使うタイムゾーン。 |
| `DISCORD_REPLY_MAX_CHARS` | `1900` | Discordへの回答1分割あたりの最大文字数。上限は2000。 |
| `DISCORD_SEND_MAX_RETRIES` | `3` | 回答の送信・編集をAdapterが再試行する回数の上限。DiscordGo内部で再試行する場合もある。 |
| `LOG_LEVEL` | `info` | `debug`、`info`、`warn`、`error`。不明な値は`info`。 |

`LLM_REASONING_EFFORT`と`LLM_THINKING_BUDGET_TOKENS`で、選択したプロファイルの既定値を上書きできます。`reasoning_effort`が有効な場合、推論強度はそのプロファイルの`supported_reasoning_efforts`にある値を指定します。Token予算の`0`は「プロファイルの既定値を使う」という意味です。既定の予算を設定しない場合はYAMLを`null`にします。各設定は`reasoning`と、それぞれの`reasoning_effort`または`thinking_budget`が両方有効な場合だけ送信します。対応するCapabilityが無効なら、環境変数やプロファイルに値があっても送信しません。

時間は`30s`、`5m`、`168h`などのGoのDuration形式で指定します。数値による環境変数の上限設定は正の整数を指定します。ただし`LLM_THINKING_BUDGET_TOKENS`は`0`も受け付けます。履歴上限と回答の分割文字数は、表の範囲も満たす必要があります。`DISCORD_SEND_MAX_RETRIES`の指定範囲は1〜10です。

相対日付・時刻はDiscord Messageの作成日時を`APP_TIMEZONE`へ変換して解釈します。Requestごとに、その日時・タイムゾーン・日付・曜日をSystem Messageへ追加します。Queue待機で時間が経過しても基準日時は変わりません。タイムゾーンの既定値は`Asia/Tokyo`で、`UTC`またはIANAの名前を使用できます。`Local`や不正な名前は起動エラーとします。日付や時刻そのものを得るためにWeb検索は行いません。

## フォアグラウンドで起動する

リポジトリ直下で実行します。プロセスは構造化したJSONログを標準出力へ書き出します。Ctrl+Cで終了を要求すると、新しいRequestの受付を止め、受付済みの処理の完了を待ってから、接続とDBを閉じます。

DiscordのRequestや配信の失敗では、`event`と`error_code`に加えて`error_detail`を記録します。機密情報を伏せた`message`、`type`、`cause_type`を含み、取得できる場合は`http_status`、`discord_code`、`cause_code`（SQLiteのコード5など）、`context_error`も記録します。`request_id`で受付・最終回答のイベントと関連付けてください。受付通知が記録されていない場合、`request_failed`の`processing_message_id`は空です。Discordには共通のエラー文だけを返します。設定された機密値や既知の機密情報の形式は伏せ、Discord APIの応答本文をログに含めません。診断メッセージは最大2048文字で切り詰め、切り詰めたことを示す文字列を付けます。

`LOG_LEVEL=DEBUG`では、Discordへの各Message送信・編集のHTTP試行を`event=discord_http_attempt`として記録します。`send_count`はReplyまたはEditごとに1から始まり、DiscordGo内部とAdapterの再試行を通算します。`request_id`と`delivery_id`で関連付けてください。分割された各Messageと各Editは、別の`delivery_id`を持ちます。ログには`operation`、`method`、`http_status`、`transport_error`、`duration_ms`を記録します。`http_status=0`はHTTP応答を取得できなかった場合です。Header、URL、本文、Token、エラー文は記録しません。このログはHTTP試行の記録であり、Discord側でMessageが作成された証拠ではありません。

開発時はソースから実行できます。

```sh
set -a
. ./.env
set +a
go run ./cmd/zii
```

バイナリをビルドして実行する場合は、次のコマンドを使用します。

```sh
go build -o ./zii ./cmd/zii
set -a
. ./.env
set +a
./zii
```

## バックグラウンドで起動する（Linux）

バイナリをビルドし、`nohup`で起動します。次の例ではリポジトリ直下にログとPIDファイルを置きます。`logs/`と`tmp/`はGit管理外です。

```sh
go build -o ./zii ./cmd/zii
mkdir -p logs tmp
set -a
. ./.env
set +a
nohup ./zii >> logs/zii.log 2>&1 < /dev/null &
echo $! > tmp/zii.pid
```

プロセスとログを確認します。

```sh
ps -p "$(cat tmp/zii.pid)" -o pid=,stat=,cmd=
tail -f logs/zii.log
```

終了する場合は`SIGTERM`を送ります。

```sh
kill -TERM "$(cat tmp/zii.pid)"
```

終了を確認したらPIDファイルを削除します。

```sh
rm -f tmp/zii.pid
```

Ziiは`SIGTERM`を処理し、実行中のRequestとQueueの終了を最大60秒待ちます。長期間稼働させる環境では、systemdなどのサービス管理機能で再起動と監視を行ってください。

## 確認とトラブルシューティング

### 繰り返し実行できるLLM評価

`go build -o bin/zii-eval ./cmd/zii-eval`で評価CLIをビルドします。Discordや会話DBを使わず、YAMLの質問セットを本番のToolLoopへ渡し、異常・応答時間を記録してJSON、CSV、Markdownのレポートを出力します。固定のTool応答でモデルを比較する方法と、実際のSearch MCPを使う疎通確認に対応しています。fixtureでもLLMには実際に通信します。評価CLIはDiscord Messageの作成日時をPromptへ付与しません。プロファイル、ウォームアップと繰り返し、指標、`compare`の使い方は[eval/README.md](eval/README.md)、責務と仕様は[zii-eval設計](design/zii-eval設計.md)を参照してください。

### アプリケーションの確認

リポジトリ直下で自動チェックを実行します。

```sh
go test ./...
go vet ./...
```

テストは偽のClientとローカルのHTTP / MCPテストサーバーを使用します。稼働中のLLM、Search MCP、Discord環境の検証にはなりません。

実際に起動する場合は、選択したプロファイルの`endpoint`で`model`に指定したモデルが読み込まれていること、必須のToken計算APIに対応していること、`SEARCH_MCP_URL`へ接続できることを確認してください。`LLM_MODEL`はプロファイルのキーであり、APIのモデル名と同じとは限りません。

実行ファイルは起動・終了の失敗を`event=fatal`、`error_code=startup_or_shutdown_error`で記録し、返されたエラー本文は記録しません。起動できない場合は、必須環境変数、プロファイルのYAML、タイムゾーン、DBディレクトリの権限、Search MCPのTool一覧を確認してください。

Discordでは、サーバー内のチャンネルでBotをメンションした質問と、DMの通常のテキストMessageに回答します。Bot、Webhook、System Messageは無視します。対象チャンネルを表示し、送信できる権限が必要です。回答は質問MessageへのReplyとして返します。

Queueに入ったRequestの実行開始時に、質問のDiscord Message IDとUser MessageをSQLiteへ原子的に保存してから受付通知を送ります。保存をCommitできた処理だけが、受付通知の送信と回答生成へ進みます。重複した質問や登録失敗では、受付通知の送信もLLMの呼び出しも行いません。Queueでは自分の順番が来るまで受付通知を送らず、履歴と回答の配信順を保ちます。Commitした質問は、受付通知や後続処理に失敗しても登録済みのままです。再試行する場合は新しい質問を投稿してください。受付通知とエラー文は会話Messageとして保存しません。

最終回答の先頭部分は受付通知を編集して表示します。残りの部分は元の質問へのReplyとして送ります。すべての配信が成功してから、受付通知のDiscord Message IDと作成日時を代表値として、Assistantの回答全文を保存します。一部しか配信できなかった場合はAssistantのターンを保存しません。配信・保存の前にはTool Callの記法や機密情報が含まれていないか検査します。検出した場合はその回答を配信せず、可能なら共通のエラー文に置き換えます。

入力は正規化したテキストのみで、最大4,000 Unicodeコードポイントを受け付けます。不正なUTF-8は拒否し、多くの制御文字を取り除きます。添付ファイルだけのMessageは処理しません。ユーザー単位のRate Limitは1分あたり5件、burstは2件です。上限を超えたRequestは無視します。会話はGuildまたはDM、チャンネルまたはスレッド、ユーザーの組み合わせで識別します。Contextへ入れる過去履歴は、設定上限内の完了した往復だけです。期限切れの会話はSQLiteに残し、新しいConversationを開始します。

生成が出力上限に達した場合、または回答本文にTool Callの記法が含まれる場合は、共通で1回だけ最終回答を再試行します。取得済みのTool Resultを最大4,096 tokensへ圧縮し、追加のToolを無効にして、`max_tokens=1536`で短い回答を求めます。推論設定の上書き（`medium`、推論用2,048 tokens）は、プロファイルが対応する場合だけ送信します。空の最終回答はエラーとし、推論本文を回答の代わりには使用しません。

## 設計書

- [Discord Bot](design/zii_Discord_BOT設計.md)
- [Orchestrator](design/zii_Discord_Bot_Orchestrator設計.md)
- [conversationsテーブル](design/zii_conversations_テーブル.md)
- [messagesテーブル](design/zii_messages_テーブル.md)
- [zii-eval](design/zii-eval設計.md)
