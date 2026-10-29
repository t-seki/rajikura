package main

// serve の HTTP API を呼ぶサブコマンド（jobs・retry・skip・record・rerecord）。DB は開かない。
// 足したら直す: CLI のサブコマンド

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/t-seki/rajikura/internal/client"
	"github.com/t-seki/rajikura/internal/domain"
)

// listJobs はジョブを一覧する。放送日・番組・パート・ID の順に並べる。
func listJobs(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("rajikura jobs", flag.ContinueOnError)
	stateFlag := fs.String("state", "", "状態で絞る（waiting, running, retrying, done, expired, skipped。カンマ区切り）")
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "使い方: rajikura jobs [-state <状態>]")
		return errUsage
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

// stateLabel は一覧の状態の列。値は API の状態のまま（skipped）で、skipped には確定の前か後か（録らない予定・録らなかった）を、
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
		call: (*client.Client).RerecordJob, refusal: rerecordRefusal,
	})
}

// jobOp は、ID ごとに serve の API を呼ぶジョブの操作（retry・skip・record・rerecord）。
type jobOp struct {
	name, done, failed string
	call               func(c *client.Client, ctx context.Context, id int64) error
	// refusal は 409 で拒まれたわけを、ジョブの今の状態から作る。
	refusal func(ctx context.Context, c *client.Client, id int64) string
}

// jobAction は op を ID ごとに行う。ID ごとに結果を 1 行出し、拒まれた ID があってもほかの ID は続ける。1 件でも拒まれたら
// エラーを返す（終了コード 1）。
func jobAction(ctx context.Context, connect func() (*client.Client, error), args []string, stdout, stderr io.Writer, op jobOp) error {
	fs := flag.NewFlagSet("rajikura "+op.name, flag.ContinueOnError)
	if err := parseFlags(fs, args, stderr); err != nil {
		return err
	}
	use := "使い方: rajikura " + op.name + " <ID> [<ID>...]"
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
			fmt.Fprintf(stdout, "ジョブ %d: 拒んだ（ID が無い）\n", id)
		case client.IsStatus(err, http.StatusConflict):
			refused++
			fmt.Fprintf(stdout, "ジョブ %d: 拒んだ（%s）\n", id, op.refusal(ctx, c, id))
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
