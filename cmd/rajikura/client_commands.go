package main

// serve の HTTP API を呼ぶサブコマンド（jobs（一覧と、ID を渡したときの 1 件の詳細）・retry・skip・record・rerecord・chapters・move）。DB は開かない。
// 足したら直す: CLI のサブコマンド

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/domain"
)

// listJobs はジョブを一覧する。放送日・番組・パート・ID の順に並べる。ID を 1 つ渡せば、そのジョブの詳細を出す（showJob）。
func listJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rajikura jobs", flag.ContinueOnError)
	stateFlag := fs.String("state", "", "状態で絞る（waiting, running, retrying, done, expired, skipped。カンマ区切り）")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	const use = "使い方: rajikura jobs [-state <状態>] または rajikura jobs <ID>"
	if fs.NArg() > 0 {
		if fs.NArg() > 1 || *stateFlag != "" {
			fmt.Fprintln(stderr, "ID は 1 つだけで、-state とは一緒に渡せない\n"+use)
			return errUsage
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "ID %q は整数ではない\n%s\n", fs.Arg(0), use)
			return errUsage
		}
		return showJob(ctx, connect, id, stdout)
	}
	var states []domain.State
	if *stateFlag != "" {
		for _, s := range strings.Split(*stateFlag, ",") {
			st, err := domain.ParseState(strings.TrimSpace(s))
			if err != nil {
				return err
			}
			states = append(states, st)
		}
	}
	c, err := connect()
	if err != nil {
		return err
	}
	jobs, err := c.ListJobs(ctx, states...)
	if err != nil {
		return err
	}
	// API は新しい放送日を上に返すので、放送日・番組・パート・ID の昇順に並べ直す。
	sort.SliceStable(jobs, func(a, b int) bool {
		x, y := jobs[a], jobs[b]
		if x.Day != y.Day {
			return x.Day < y.Day
		}
		if x.ShowName != y.ShowName {
			return x.ShowName < y.ShowName
		}
		if x.Part != y.Part {
			return x.Part < y.Part
		}
		return x.ID < y.ID
	})

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\t番組\t放送日\t状態\t次の試行\t試行回数\t録音期限\t保存先\t直近のエラー")
	for _, j := range jobs {
		show := j.ShowName
		if j.Part > 0 {
			show = fmt.Sprintf("%s (%d)", show, j.Part)
		}
		next := "-"
		if j.State == string(domain.StateWaiting) || j.State == string(domain.StateRetrying) {
			next = formatTime(j.NextAttemptAt)
		}
		out := orDash(j.OutputPath)
		if j.MissingCount > 0 {
			out += fmt.Sprintf("（欠落 %d）", j.MissingCount)
		}
		lastErr := "-"
		if j.LastError != "" {
			lastErr = fmt.Sprintf("[%s] %s", j.LastFailedStage, oneLine(j.LastError))
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			j.ID, show, j.Day, stateLabel(j), next, j.Attempts, formatTime(j.Deadline), out, lastErr)
	}
	return tw.Flush()
}

// showJob はジョブ 1 件の詳細を、人が読む形で出す（Web UI のジョブの詳細と同じ範囲。プレーヤーと進捗は出さない）。上に概要を
// 「項目: 値」の行で並べ、その下に放送回・試行の履歴・チャプターの書き込みの履歴・保存先の移しの履歴を見出し付きの表で出す（履歴が空なら「なし」）。
// 無い ID（404）は終了コード 1。
func showJob(ctx context.Context, connect func() (*client.Client, error), id int64, stdout io.Writer) error {
	c, err := connect()
	if err != nil {
		return err
	}
	j, err := c.GetJobDetail(ctx, id)
	if client.IsStatus(err, http.StatusNotFound) {
		return fmt.Errorf("ジョブ %d: ID が無い", id)
	}
	if err != nil {
		return err
	}

	show := j.ShowName
	if j.Part > 0 {
		show = fmt.Sprintf("%s (%d)", show, j.Part)
	}
	line := func(k, v string) { fmt.Fprintf(stdout, "%s: %s\n", k, v) }
	line("ID", strconv.FormatInt(j.ID, 10))
	line("番組", show)
	line("放送日", j.Day)
	line("状態", stateLabel(j.Job))
	if j.State == string(domain.StateWaiting) || j.State == string(domain.StateRetrying) {
		line("次の試行", formatTime(j.NextAttemptAt))
	}
	line("試行回数", strconv.Itoa(j.Attempts))
	line("録音期限", formatTime(j.Deadline))
	out := orDash(j.OutputPath)
	if j.MissingCount > 0 {
		out += fmt.Sprintf("（欠落 %d）", j.MissingCount)
	}
	line("保存先", out)
	dur := "記録なし"
	if j.DurationSeconds > 0 {
		dur = formatDuration(j.DurationSeconds)
	}
	line("長さ", dur)
	if label, ok := chaptersLabels[j.Chapters]; ok {
		line("チャプター", label)
	} else if j.Chapters != "" {
		line("チャプター", j.Chapters)
	}
	lastErr := "-"
	if j.LastError != "" {
		lastErr = fmt.Sprintf("[%s] %s", j.LastFailedStage, oneLine(j.LastError))
	}
	line("直近のエラー", lastErr)
	line("受け付ける操作", jobOperations(j.Job))
	if j.ChaptersRedoRequested {
		fmt.Fprintln(stdout, "チャプターの入れ直しを待っている。")
	}
	if j.MoveRequested {
		fmt.Fprintln(stdout, "保存先の移しを待っている。")
	}
	// 補足（当たるときだけ）。文は web/src/jobs/JobDetailModal.tsx に揃える。
	if j.Live {
		fmt.Fprintln(stdout, "ライブ録音のジョブ（タイムフリーの対象外の放送回を含むので、放送中に録る。放送の時刻に rajikura が動いている必要がある）。")
	}
	if j.State == string(domain.StateSkipped) {
		fmt.Fprintln(stdout, skipReason(j))
	}
	if j.SaveRetryUntil != nil {
		fmt.Fprintf(stdout, "録れた分を保存できなかったので、保存を自動でやり直している（%s まで）。\n", formatTime(*j.SaveRetryUntil))
	}
	if j.KeepUntil != nil {
		fmt.Fprintf(stdout, "録れた分を %s まで残している。保存先などの原因を直してから rajikura retry を打つと、保存からやり直す。\n", formatTime(*j.KeepUntil))
	}

	section := func(title string, header string, rows [][]string) error {
		fmt.Fprintf(stdout, "\n%s\n", title)
		if len(rows) == 0 {
			fmt.Fprintln(stdout, "なし")
			return nil
		}
		tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, header)
		for _, r := range rows {
			fmt.Fprintln(tw, strings.Join(r, "\t"))
		}
		return tw.Flush()
	}
	var airings [][]string
	for _, a := range j.Airings {
		missing := "-"
		if a.Missing {
			missing = "欠落"
		}
		airings = append(airings, []string{a.StationID, formatTime(a.Start), formatTime(a.End), oneLine(a.Title), missing})
	}
	if err := section("放送回", "局\t開始\t終わり\t題\t欠落", airings); err != nil {
		return err
	}
	var attempts [][]string
	for _, a := range j.AttemptLog {
		stage, errText, d := "-", "-", "-"
		if a.FailedStage != "" {
			stage = stageLabel(a.FailedStage)
		}
		if a.Error != "" {
			errText = oneLine(a.Error)
		}
		if a.DurationSeconds > 0 {
			d = formatDuration(a.DurationSeconds)
		}
		attempts = append(attempts, []string{formatTimeSec(a.StartedAt), formatTimeSec(a.EndedAt), stage, errText, d})
	}
	if err := section("試行の履歴", "始まり\t終わり\t失敗した段階\tエラー\t長さ", attempts); err != nil {
		return err
	}
	var writes [][]string
	for _, w := range j.ChapterWrites {
		by, ok := chapterWriteByLabels[w.By]
		if !ok {
			by = w.By
		}
		errText := "-"
		if w.Error != "" {
			errText = oneLine(w.Error)
		}
		writes = append(writes, []string{formatTimeSec(w.At), by, chapterWriteResultText(w), errText})
	}
	if err := section("チャプターの書き込みの履歴", "時刻\t誰が\t結果\tエラー", writes); err != nil {
		return err
	}
	var moves [][]string
	for _, m := range j.OutputMoves {
		errText := "-"
		if m.Error != "" {
			errText = oneLine(m.Error)
		}
		to := m.To
		if to == m.From {
			// album_rewritten は移す前と移し先が同じパス。同じパスを 2 回並べず、移し先は「-」にする。
			to = ""
		}
		moves = append(moves, []string{formatTimeSec(m.At), outputMoveResultText(m.Result), orDash(m.From), orDash(to), errText})
	}
	return section("保存先の移しの履歴", "時刻\t結果\t移す前\t移し先\tエラー", moves)
}

// outputMoveResultText は保存先の移しの結果の言葉（web/src/jobs/jobsApi.ts の outputMoveResultLabels と同じ文）。知らない値はそのまま返す。
func outputMoveResultText(result string) string {
	switch result {
	case "moved":
		return "移した"
	case "album_rewritten":
		return "保存先は同じなので、アルバムの名前だけ書き換えた"
	case "target_exists":
		return "移し先に同じ名前のファイルがあるので移さなかった"
	case "no_file":
		return "保存先にファイルが無いので移さなかった"
	case "not_show_path":
		return "保存先が番組の名前の形でないので移さなかった"
	case "failed":
		return "移せなかった（ファイルは前のまま）"
	}
	return result
}

// 値の書き方は web/src/jobs/jobsApi.ts の chaptersLabels・chapterWriteByLabels・chapterWriteResultText と同じ文にする。
var chaptersLabels = map[string]string{"added": "入った", "waiting": "待っている", "not_added": "入らなかった"}

var chapterWriteByLabels = map[string]string{"refetch": "チャプターの取り直し", "redo": "チャプターの入れ直し"}

func chapterWriteResultText(w client.JobChapterWrite) string {
	switch w.Result {
	case "written":
		return fmt.Sprintf("%d 件を書き込んだ", w.Count)
	case "not_fetched":
		return "チャプターを取れなかったので書かなかった"
	case "failed":
		return "書き込めなかった（ファイルは前のまま）"
	}
	return w.Result
}

// stageLabel は試行の失敗した段階の言葉（web/src/jobs/format.ts の stageLabel と同じ）。知らない値はそのまま返す。
func stageLabel(stage string) string {
	switch stage {
	case "fetch":
		return "入手"
	case "verify":
		return "検証"
	case "save":
		return "保存"
	}
	return stage
}

// formatDuration は秒を「1時間30分」の形にする（web/src/jobs/format.ts の formatDuration と同じ）。
func formatDuration(sec float64) string {
	m := int(math.Round(sec / 60))
	if h := m / 60; h > 0 {
		return fmt.Sprintf("%d時間%d分", h, m%60)
	}
	return fmt.Sprintf("%d分", m)
}

// skipReason は skipped のジョブを、誰がなぜ録らないと決めたかの文（web/src/jobs/JobDetailModal.tsx の skipReason と同じ）。
func skipReason(j client.JobDetail) string {
	what := "録らなかった"
	if !j.Settled {
		what = "録らない予定にした"
	}
	if j.ByPerson {
		return "人が録らないと決めたので、" + what + "。"
	}
	seen := map[string]bool{}
	var titles strings.Builder
	for _, a := range j.Airings {
		if !seen[a.Title] {
			seen[a.Title] = true
			titles.WriteString("「" + a.Title + "」")
		}
	}
	return fmt.Sprintf("番組表の番組名%sが番組「%s」の名前で始まらないので、%s。", titles.String(), j.ShowName, what)
}

// jobOperations は、いま打てるサブコマンドの名前を並べる（API の印と対応。無ければ「なし」）。
func jobOperations(j client.Job) string {
	var ops []string
	for _, o := range []struct {
		ok   bool
		name string
	}{{j.Retryable, "retry"}, {j.Skippable, "skip"}, {j.Recordable, "record"}, {j.Rerecordable, "rerecord"}, {j.ChaptersRedoable, "chapters"},
		{j.Movable, "move"}} {
		if o.ok {
			ops = append(ops, o.name)
		}
	}
	if len(ops) == 0 {
		return "なし"
	}
	return strings.Join(ops, "・")
}

func formatTimeSec(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(domain.JST).Format("2006-01-02 15:04:05")
}

// stateLabel は一覧の状態の列と、詳細（jobs <ID>）の「状態」の行に使う。値は API の状態のまま（skipped）で、skipped には確定の前か後か（録らない予定・録らなかった）を、
// 人が決めたジョブには「人が決めた」を、ライブで録るジョブ（API の live）には「ライブ録音」を括弧で添える。
func stateLabel(j client.Job) string {
	var notes []string
	if j.State == string(domain.StateSkipped) {
		if j.Settled {
			notes = append(notes, "録らなかった")
		} else {
			notes = append(notes, "録らない予定")
		}
	}
	if j.ByPerson {
		notes = append(notes, "人が決めた")
	}
	if j.Live {
		notes = append(notes, "ライブ録音")
	}
	if len(notes) == 0 {
		return j.State
	}
	return j.State + "（" + strings.Join(notes, "・") + "）"
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(domain.JST).Format("2006-01-02 15:04")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// oneLine は改行とタブを空白にする（表が崩れないように）。
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// retryJobs は retrying / expired / 確定した skipped（録らなかった）のジョブの次の試行をすぐに行わせる（手動再試行）。ID ごとに serve の API を呼ぶ。
// 状態は retrying、次の試行は今、試行回数は 0 になり、serve が録音のループを起こして拾う（次のポーリングを待たない）。実行中のジョブや、先に次の試行の時刻を過ぎたジョブがあれば、その後になる。
// ID ごとに結果を 1 行出し、拒まれた ID があってもほかの ID は続ける。1 件でも拒まれたら終了コード 1。
// 足したら直す: ジョブの状態の移り方
func retryJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	return jobAction(ctx, connect, args, stdout, stderr, jobOp{
		name: "retry", done: "再試行にした", failed: "再試行にできなかった",
		call: (*client.Client).RetryJob, refusal: retryRefusal,
	})
}

// skipJobs は、試行がまだ 1 度も無い waiting のジョブを「録らない」にする（ADR 0019）。放送の前なら録らない予定、最初の試行の
// 時刻を過ぎていれば録らなかったになり、人が決めたジョブとして、その後の番組表の同期で番組名によって戻らない。
// 結果の出し方と終了コードは retryJobs と同じ。
func skipJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	return jobAction(ctx, connect, args, stdout, stderr, jobOp{
		name: "skip", done: "録らないにした", failed: "録らないにできなかった",
		call: (*client.Client).SkipJob, refusal: skipRefusal,
	})
}

// recordJobs は、skipped のジョブを「録る」に戻す（ADR 0019）。録らない予定なら録音予定（waiting）に戻して人が決めたジョブにし、
// 録らなかったなら手動再試行と同じく retrying にする。結果の出し方と終了コードは retryJobs と同じ。
func recordJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	return jobAction(ctx, connect, args, stdout, stderr, jobOp{
		name: "record", done: "録るにした", failed: "録るにできなかった",
		call: (*client.Client).RecordJob, refusal: recordRefusal,
	})
}

// rerecordJobs は、録音済み（done）のタイムフリーから録るジョブを録り直す（ADR 0025）。録音期限の内側のものだけで、ライブ録音の
// ジョブは録り直せない。状態は手動再試行と同じく retrying、次の試行は今、試行回数は 0 になり、serve が録音のループを起こして
// 拾う。前の保存先と同じ名前で保存するときは上書きする。結果の出し方と終了コードは retryJobs と同じ。
// 足したら直す: ジョブの状態の移り方
func rerecordJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	return jobAction(ctx, connect, args, stdout, stderr, jobOp{
		name: "rerecord", done: "録り直しにした", failed: "録り直しにできなかった",
		call: (*client.Client).RerecordJob, refusal: rerecordRefusal, refusalByMessage: rerecordRefusalByMessage,
	})
}

// chaptersJobs は、録音済み（done）のタイムフリーから録ったジョブのチャプターの入れ直しを頼む（用語集。ADR 0033）。serve の録音の
// ループが、radiko から音声を取り直さずに、保存したファイルにチャプターを書き込む（結果はジョブのチャプターの値と、チャプターの
// 書き込みの履歴に出る。CLI は待たない）。ID を渡せばそのジョブに当て、チャプターが入ったジョブでも置き換える。ライブ録音のジョブは
// 断る。結果の出し方と終了コードは retryJobs と同じ。-all なら、チャプターの値が値なしか入らなかったのジョブの全部に当て
// （入った・待っているは飛ばす。放送日では絞らない）、受け付けたジョブの数を出す。-all と ID は一緒に渡せない。
func chaptersJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	const use = "使い方: rajikura chapters <ID> [<ID>...] または rajikura chapters -all"
	fs := flag.NewFlagSet("rajikura chapters", flag.ContinueOnError)
	all := fs.Bool("all", false, "チャプターが入っていない（値なし・入らなかった）録音済みのタイムフリーのジョブの全部に当てる")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if !*all {
		return jobAction(ctx, connect, fs.Args(), stdout, stderr, jobOp{
			name: "chapters", use: use, done: "チャプターの入れ直しを受け付けた", failed: "チャプターの入れ直しにできなかった",
			call: (*client.Client).RedoJobChapters, refusal: chaptersRefusal,
		})
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "-all と ID は一緒に渡せない\n"+use)
		return errUsage
	}
	c, err := connect()
	if err != nil {
		return err
	}
	n, err := c.RedoAllChapters(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "チャプターの入れ直しを %d 件のジョブで受け付けた\n", n)
	return nil
}

// moveJobs は、ジョブの保存先の移しを頼む（用語集。ADR 0039）。serve の録音のループが、音声を取り直さずに、保存したファイルを
// 今の番組の名前のフォルダ・ファイル名へ移し、アルバムの名前も今の番組の名前にする（結果はジョブの保存先と、保存先の移しの履歴に
// 出る。CLI は待たない）。-show を渡せば、ジョブをその番組（今あるもの）に付け替えてから移す。受け付けるのは保存先のあるジョブだけで、
// 録音中のジョブ・無い番組は断る。結果の出し方と終了コードは retryJobs と同じ。
func moveJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	const use = "使い方: rajikura move [-show <番組>] <ID> [<ID>...]"
	fs := flag.NewFlagSet("rajikura move", flag.ContinueOnError)
	show := fs.String("show", "", "移し先の番組の名前（今ある番組。空ならジョブの今の番組）")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	name := strings.TrimSpace(*show)
	return jobAction(ctx, connect, fs.Args(), stdout, stderr, jobOp{
		name: "move", use: use, done: "保存先の移しを受け付けた", failed: "保存先の移しにできなかった",
		call:    func(c *client.Client, ctx context.Context, id int64) error { return c.MoveJob(ctx, id, name) },
		refusal: moveRefusal,
		notFoundByMessage: func(msg string) string {
			if msg == "番組が無い" {
				return fmt.Sprintf("番組「%s」が無い", name)
			}
			return ""
		},
	})
}

// moveRefusal は、保存先の移しを拒まれた（409）わけを、ジョブの今の状態から作る（domain.CheckOutputMove と同じ）。
func moveRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "保存先を移せない状態"
	}
	if j.OutputPath == "" {
		return "まだ保存していないジョブで、保存先を移せるのは保存先のあるジョブだけ"
	}
	return fmt.Sprintf("状態が %s で、録音中のジョブは保存先を移せない（録音が終わってから頼む）", stateLabel(j))
}

// jobOp は、ID ごとに serve の API を呼ぶジョブの操作（retry・skip・record・rerecord・chapters・move）。
type jobOp struct {
	name, done, failed string
	// use は使い方の誤りのときに出す文（空なら「使い方: rajikura <name> <ID> [<ID>...]」）。
	use  string
	call func(c *client.Client, ctx context.Context, id int64) error
	// refusal は 409 で拒まれたわけを、ジョブの今の状態から作る。
	refusal func(ctx context.Context, c *client.Client, id int64) string
	// refusalByMessage は、ジョブの状態では見分けられない 409 のわけを、serve の答えの文（APIError.Message）から作る。
	// 空を返したら refusal を使う。nil なら使わない。
	refusalByMessage func(msg string) string
	// notFoundByMessage は、404 のわけを serve の答えの文から作る（ジョブのほかにも無いものがある操作。move の番組）。空を返したら
	// 「ID が無い」にする。nil なら使わない。
	notFoundByMessage func(msg string) string
}

// jobAction は op を ID ごとに行う。ID ごとに結果を 1 行出し、拒まれた ID があってもほかの ID は続ける。1 件でも拒まれたら
// エラーを返す（終了コード 1）。
func jobAction(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer, op jobOp) error {
	fs := flag.NewFlagSet("rajikura "+op.name, flag.ContinueOnError)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	use := op.use
	if use == "" {
		use = "使い方: rajikura " + op.name + " <ID> [<ID>...]"
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, use)
		return errUsage
	}
	ids := make([]int64, 0, fs.NArg())
	for _, a := range fs.Args() {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "ID %q は整数ではない\n%s\n", a, use)
			return errUsage
		}
		ids = append(ids, id)
	}

	c, err := connect()
	if err != nil {
		return err
	}
	refused := 0
	for _, id := range ids {
		err := op.call(c, ctx, id)
		switch {
		case err == nil:
			fmt.Fprintf(stdout, "ジョブ %d: %s\n", id, op.done)
		case client.IsStatus(err, http.StatusNotFound):
			refused++
			why := ""
			var ae *client.APIError
			if op.notFoundByMessage != nil && errors.As(err, &ae) {
				why = op.notFoundByMessage(ae.Message)
			}
			if why == "" {
				why = "ID が無い"
			}
			fmt.Fprintf(stdout, "ジョブ %d: 拒んだ（%s）\n", id, why)
		case client.IsStatus(err, http.StatusConflict):
			refused++
			why := ""
			var ae *client.APIError
			if op.refusalByMessage != nil && errors.As(err, &ae) {
				why = op.refusalByMessage(ae.Message)
			}
			if why == "" {
				why = op.refusal(ctx, c, id)
			}
			fmt.Fprintf(stdout, "ジョブ %d: 拒んだ（%s）\n", id, why)
		default:
			return err
		}
	}
	if refused > 0 {
		return fmt.Errorf("%d 件のジョブを%s", refused, op.failed)
	}
	return nil
}

// retryRefusal は、手動再試行を拒まれた（409）わけを、ジョブの今の状態から作る。
// retrying・expired・確定した skipped は状態としては受け付けるので、拒まれたのは録音期限を過ぎているため（store.ManualRetry と同じ）。
// retrying・expired のライブで録るジョブは、保存か検証の段階の失敗から 30 日の間（録れた分を残している間）は録音期限を過ぎても
// 受け付けるので、拒まれたのはそれにも当たらないため。
// 足したら直す: ジョブの状態の移り方
func retryRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "再試行できない状態"
	}
	if j.State == string(domain.StateSkipped) && !j.Settled {
		return "録らない予定のジョブで、録るなら rajikura record を使う"
	}
	if j.State == string(domain.StateSkipped) {
		return "録音期限を過ぎている"
	}
	if j.State == string(domain.StateRetrying) || j.State == string(domain.StateExpired) {
		return "録音期限を過ぎている。ライブ録音で録れた分を残しているジョブなら、保存か検証の段階の失敗から 30 日の間は再試行できるが、それも過ぎているか、録れた分が無い"
	}
	return fmt.Sprintf("状態が %s で、再試行できるのは retrying・expired・skipped（録らなかった）だけ", j.State)
}

// rerecordRefusalByMessage は、自分のエリアの局一覧を取れずに拒まれた（domain.ErrRerecordAreaUnknown）ときのわけを返す
// （ジョブの状態からは見分けられないので、serve の答えの文で見分ける）。ほかは空。
func rerecordRefusalByMessage(msg string) string {
	if strings.HasPrefix(msg, domain.ErrRerecordAreaUnknown.Error()) {
		return "自分のエリアの局一覧を取れないので、いまは録り直せない。後でやり直す"
	}
	return ""
}

// rerecordRefusal は、録り直しを拒まれた（409）わけを、ジョブの今の状態から作る（domain.CheckRerecord と同じ）。
func rerecordRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "録り直せない状態"
	}
	if j.State != string(domain.StateDone) {
		return fmt.Sprintf("状態が %s で、録り直せるのは done（録音済み）だけ", stateLabel(j))
	}
	if j.Live {
		return "ライブ録音のジョブで、ライブ録音は録り直せない"
	}
	return "録音期限を過ぎている"
}

// chaptersRefusal は、チャプターの入れ直しを拒まれた（409）わけを、ジョブの今の状態から作る（domain.CheckChapterRedo と同じ）。
func chaptersRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "チャプターを入れ直せない状態"
	}
	if j.State != string(domain.StateDone) {
		return fmt.Sprintf("状態が %s で、チャプターを入れ直せるのは done（録音済み）だけ", stateLabel(j))
	}
	return "ライブ録音のジョブで、ライブ録音のファイルにはチャプターを入れない"
}

// skipRefusal は、「録らない」を拒まれた（409）わけを、ジョブの今の状態から作る（store.SkipByPerson と同じ）。
func skipRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "録らないにできない状態"
	}
	if j.State == string(domain.StateWaiting) {
		return fmt.Sprintf("試行が %d 回あり、録らないにできるのは試行がまだ無い waiting だけ", j.Attempts)
	}
	return fmt.Sprintf("状態が %s で、録らないにできるのは試行がまだ無い waiting だけ", stateLabel(j))
}

// recordRefusal は、「録る」を拒まれた（409）わけを、ジョブの今の状態から作る（store.RecordByPerson と同じ）。
func recordRefusal(ctx context.Context, c *client.Client, id int64) string {
	j, err := c.GetJob(ctx, id)
	if err != nil {
		return "録るにできない状態"
	}
	if j.State == string(domain.StateSkipped) {
		return "録らなかったのジョブで、録音期限を過ぎている"
	}
	return fmt.Sprintf("状態が %s で、録るにできるのは skipped だけ", j.State)
}
