# Zii Ver1.2実装記録

## Phase 1 — 方針

2026-10-10に取得したorigin/mainの8764df1を基点に、future_20261010で実装する。mainへのマージは行わない。

Identity、ユーザー向けCapability、Release Manifestはinternal/identity/manifest.yamlにまとめ、go:embedで本番と評価のバイナリに組み込む。未知のフィールド、不正YAML、複数文書、空欄、重複、未知の状態、現在版の欠落やplanned状態を拒否する。埋め込みデータが不正なら起動時に失敗し、古い情報へのフォールバックは行わない。外部設定や環境変数をManifestへ取り込まない。

System Promptは既存のSearch Policy、Trust Boundary、機密情報非開示を保ち、Manifestから生成した情報を追加する。本番ServiceとEvalは同じPrompt関数を使う。検索の利用可否はToolLoopで選択されたモデルのTools設定と登録済みToolに合わせる。基盤モデルについて公開するのはAPIのモデル識別子だけとし、Endpointや内部設定は渡さない。モデル識別子はサーバー設定上の名前であり、モデルの開発元や正確な重みを保証しない。

ServiceConfig.SystemPromptを指定した場合も共有Promptを残し、追加の信頼済み指示として末尾へ付ける。既存コードにこの設定を使う本番呼び出しはない。Discord固有の日時Contextは既存どおりメッセージ作成時刻から生成し、EvalのIdentity情報・組み立てロジックと区別する。

指示書のconfig/zii_manifest.yamlは配置例のため、配布時の配置漏れや本番・評価の読み込み先の違いを避ける埋め込み方式を選んだ。旧ロードマップの情報選択案に対し、今回の指示書に従い小規模な全6版をSystem Contextへ入れる。Ver1.2はこのバイナリの機能版としてreleasedとするが、mainへの公開や本番デプロイを意味しない。将来版の日付は未定とする。

変更範囲はinternal/identity、internal/orchestratorのPromptとService、internal/evalのPrompt参照・ハッシュ、eval/testcases、READMEと評価資料。Search MCP、Queue、保存、モデルプロファイル、Tool Loopの実行処理は既存のまま使う。

## Phase 2〜4 — 実施内容

Manifest検証テストを先に追加し、未実装による失敗を確認してから最小実装した。Prompt統合、Eval統合もテストを先に追加した。Promptの組み立て、検索無効モデル、信頼境界、模擬LLMでの自己紹介・リリース問答、保存、本番と評価の一致とハッシュを検証した。評価セットにID-01〜20を追加し、通常会話・検索の既存ケースも実行した。

実Qwenの自己紹介・リリース問答では不要なTool呼び出しや基盤モデルとの混同が見られなかったため、アプリ側の決定的応答、Intent Classifier、新規Toolは追加しなかった。これは今回の1回の評価結果であり、全モデルや任意の入力に対する保証ではない。

## 変更前の回帰結果

最新mainのgo test ./...は、TestYAMLValidationAndShippedCasesだけが失敗した。Qwenの既定reasoning_effortが設定上xhighなのに、テストがmediumを固定値として要求していたためである。モデル設定は変更せず、テストを選択プロファイルの構造体全体との照合へ変更した。

Qwenは127.0.0.1:8080の/v1/modelsで識別子qwenを確認した。MinistralのプロファイルEndpointはsandbox外でも接続を拒否した。Ministralの実モデル検証は未実施である。

## Phase 5 — テスト結果と成果物

| 検証 | 結果 |
| --- | --- |
| go test ./... | PASS。Identity、Eval、本番経路、既存LLM・Search MCP・Queue・SQLite・Discord adapterを含む |
| go vet ./... | PASS |
| go test -race ./internal/orchestrator ./internal/eval | PASS |
| go build -o /tmp/zii-v1.2 ./cmd/zii | PASS |
| go build -o /tmp/zii-v1.2-eval ./cmd/zii-eval | PASS |
| zii-eval check --profile eval/profiles/example.yaml | PASS、43ケース |
| gofmt -l cmd internal / git diff --check | 出力なし、PASS |
| 実Qwen、category identity、warmup 0、runs 1 | 19 PASS / 1 WARN / 0 FAIL |
| 実Qwen、既存category basic | 4 PASS / 0 WARN / 0 FAIL |
| 実Qwen、既存category tool-search | 3 PASS / 0 WARN / 0 FAIL |
| 実Qwen、既存category multi-tool | 1 PASS / 0 WARN / 0 FAIL |

評価はeval/profiles/example.yamlを使用した。Qwenの既定reasoning_effortはxhigh、Toolsとparallel_tool_callsは有効、Search MCP応答はfixtureである。実LLMに問い合わせているが、実Search MCPの検索品質・可用性を測った結果ではない。単体テストには模擬LLMを使った。sandbox内の最初の実モデル評価はtransport_errorで20件失敗したため、sandbox外で再実行した。HTTPサーバーを起動するテストもsandbox外で実行した。

実行コマンドは次の通り。CLIの標準設定どおり、WARNのあるIdentity評価の終了コードは1、他の実モデル評価は0だった。

```bash
/tmp/zii-v1.2-eval run --profile eval/profiles/example.yaml --category identity --warmup 0 --runs 1 --save-answers --out reports/v1.2-identity-qwen-host
/tmp/zii-v1.2-eval run --profile eval/profiles/example.yaml --category basic --warmup 0 --runs 1 --save-answers --out reports/v1.2-regression-basic
/tmp/zii-v1.2-eval run --profile eval/profiles/example.yaml --category tool-search --warmup 0 --runs 1 --save-answers --out reports/v1.2-regression-search
/tmp/zii-v1.2-eval run --profile eval/profiles/example.yaml --category multi-tool --warmup 0 --runs 1 --save-answers --out reports/v1.2-regression-multi-tool
```

各reportsディレクトリのreport.json、summary.md、samples.jsonlに結果を保存した。reportsはGit管理外。最初の通信失敗はreports/v1.2-identity-qwenに残した。今回の測定は回答と経路の確認に使用した。通常会話の評価がIdentity評価と一部同時に動いたため、遅延の比較や性能改善の根拠には使わない。変更前との同条件compareは実施していない。

保存したIdentity全20回答を確認した。Ziiとしての自己紹介、1.2の説明、releasedとplannedの区別、未定の提供日、画像生成と永続Memoryの未提供、API識別子qwenとZiiの区別、未知の4.0の未定扱いを確認した。id_18を除く19件はTool呼び出し0回で、通常のGo解説ではIdentityを不必要に出していない。

id_18はper_tool_limit_exceeded:search_webのWARNだった。Qwenがsearch_webを3回要求し、設定上限1回を超えた2回を既存Orchestratorが拒否した。許可された検索1回の結果から最終回答は生成できた。Toolの上限制御は維持し、この1ケースのために上限を緩める変更は行っていない。保存回答には予約ドメインの本文を取得できないという説明もあり、fixtureにはfetch_pageの応答があるため、これは取得処理で確認した事実ではない。検索回答の品質上の制約として残す。

日本語資料はyomiyasuのリンターで確認した。実装記録はPASS、評価READMEは文末の連続に関するNOTICEのみ。技術上の意味を保つため既存の説明は維持した。

## 変更ファイル

| ファイル | 変更内容 |
| --- | --- |
| README.md | Ziiの説明、Manifest更新・再ビルド手順、モデル設定の現状 |
| docs/ver1.2-implementation.md | 方針、指示書との差異、検証結果、PR要約 |
| internal/identity/manifest.yaml | Identity、Capability、全6版のRelease Manifest |
| internal/identity/manifest.go | 埋め込み、厳格な読み込み、バリデーション |
| internal/identity/manifest_test.go | 正常系、不正設定、共有状態を変更しない検証 |
| internal/orchestrator/system_prompt.go | 共有Prompt関数、Manifestと実行時利用可否の合成 |
| internal/orchestrator/service.go | 共有Promptの使用、追加指示の扱い |
| internal/orchestrator/identity_test.go | 問答入力、検索無効、履歴分離、保存の契約検証 |
| internal/orchestrator/system_prompt_test.go | 合成後のPromptで既存ポリシーと機密情報除外を確認 |
| internal/orchestrator/context_builder_test.go | 合成後のPromptを使った信頼境界検証 |
| internal/orchestrator/tool_loop_test.go | 合成後のPromptと検索結果によるIdentity上書きの防止検証 |
| internal/eval/runner.go | 共有Prompt、実行時情報を含むレポート・sampleのハッシュ |
| internal/eval/identity_test.go | 本番と評価のPrompt一致、実送信内容とハッシュの一致 |
| internal/eval/eval_test.go | 43ケース検査、モデル設定との照合 |
| eval/testcases/identity.yaml | ID-01〜20の自己紹介・リリース・検索回帰ケース |
| eval/README.md | 新カテゴリ、ケース数、Prompt共有とハッシュの説明 |

## 未検証事項・持ち越し

Ministralなど別モデル、tools:falseモデルでの実回答、Discord Gateway経由のE2E、実Search MCPのE2Eは未検証。tools:falseのPromptと定義の扱い、Discordの返信分割・マスキング、保存、既存Search MCPクライアントは自動テストで確認した。実モデルの意味的な正確さを模擬テストのPASSで代替しない。

ニュース検索の過剰なTool要求と、取得していないページに関する説明は残る。Ver1.5のAgent強化を今回実装したとは扱わない。User Memory、Memory最適化、Visual Output、画像生成も今後の計画のままである。本番デプロイ、commit、push、mainへのマージは行っていない。

## PR用要約

タイトル案は「Add Zii Ver1.2 identity and release manifest」。

基盤LLMが自分をQwenなどとして説明する問題に対し、アプリ管理のIdentity・Capability・Release Manifestを追加した。本番ServiceとEvaluation Harnessで同じSystem Promptを組み立て、モデルのTool対応と登録Toolから検索の利用可否を反映する。予定機能と現在機能を区別し、評価レポートにも実送信Promptのハッシュを保存する。

全体テスト、vet、指定raceテスト、両CLIビルドはPASS。実Qwenの新設20件は19 PASS / 1 WARN / 0 FAIL、既存の通常会話・検索・3段階Toolケース8件はすべてPASS。WARNはニュース検索でのsearch_web要求上限超過で、超過分は既存Orchestratorが拒否した。Ministral、Discord、実Search MCPのE2Eは未検証。
