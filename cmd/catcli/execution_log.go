package main

import (
	"AgentCLI/internal/execution"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func loadExecutionLog(metadataDir, conversationID string) (*execution.JSONLEventStore, []execution.RunState, error) {
	store, err := execution.NewJSONLEventStore(filepath.Join(metadataDir, "execution"))
	if err != nil {
		return nil, nil, err
	}
	runs, err := store.LoadRuns(context.Background(), conversationID)
	if err != nil {
		return nil, nil, err
	}
	return store, runs, nil
}

func resumeExecution(ctx context.Context, run execution.RunState, react, plan func(context.Context, execution.RunState) (string, error)) (string, error) {
	if run.Kind == execution.PlanAndExecuteRun || run.Plan != nil {
		return plan(ctx, run)
	}
	if run.Kind == "" || run.Kind == execution.ReActRun {
		return react(ctx, run)
	}
	return "", fmt.Errorf("unsupported recovery kind %q", run.Kind)
}

func promptInterruptedRun(ctx context.Context, store execution.EventStore, conversationID string, runs []execution.RunState, reader interface{ ReadString(byte) (string, error) }, output io.Writer, resume func(context.Context, execution.RunState) (string, error)) error {
	if len(runs) == 0 {
		return nil
	}
	run := runs[len(runs)-1]
	if run.Status != execution.Interrupted {
		return nil
	}
	unknown := 0
	for _, call := range run.Calls {
		if call.Status == execution.Unknown {
			unknown++
		}
	}
	fmt.Fprintf(output, "发现上次未完成的任务（run=%s）：\n%s\n已记录调用：%d，结果不确定：%d\n", run.RunID, run.Input, len(run.Calls), unknown)
	for {
		fmt.Fprint(output, "是否继续这个任务？[y=继续 / N=放弃]：")
		line, readErr := reader.ReadString('\n')
		// EOF or interrupted input is not an explicit decision to abandon.
		if readErr != nil {
			return readErr
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes", "继续":
			answer, err := resume(ctx, run)
			if err != nil {
				return err
			}
			fmt.Fprintln(output, answer)
			return nil
		case "", "n", "no", "放弃":
			_, err := store.Append(ctx, execution.Event{
				EventHeader: execution.EventHeader{ConversationID: conversationID, RunID: run.RunID, Type: execution.RunAbandoned},
				Run:         &execution.RunEventData{Error: "user chose to abandon interrupted run"},
			})
			if err != nil {
				return err
			}
			fmt.Fprintln(output, "已放弃该任务。")
			return nil
		default:
			fmt.Fprintln(output, "请输入 y 继续，或 n 放弃。")
		}
	}
}
