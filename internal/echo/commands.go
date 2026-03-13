package echo

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/SemSwitch/Nexis-Echo/internal/store"
)

func PrintStatus(ctx context.Context, writer io.Writer, database *store.Store) error {
	captures, err := database.ListCaptures(ctx)
	if err != nil {
		return err
	}
	if len(captures) == 0 {
		_, err = fmt.Fprintln(writer, "no captures")
		return err
	}

	for _, capture := range captures {
		last := ""
		if !capture.LastEventAt.IsZero() {
			last = capture.LastEventAt.Format("2006-01-02 15:04:05")
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n", capture.Name, capture.Provider, capture.Mode, capture.Status, last); err != nil {
			return err
		}
	}
	return nil
}

func PrintJobs(ctx context.Context, writer io.Writer, database *store.Store, status string, limit int) error {
	jobs, err := database.ListJobs(ctx, strings.TrimSpace(status), limit)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		_, err = fmt.Fprintln(writer, "no jobs")
		return err
	}
	for _, job := range jobs {
		text := strings.TrimSpace(job.SummaryText)
		if text == "" {
			text = strings.TrimSpace(job.SpeechText)
		}
		if len(text) > 80 {
			text = text[:77] + "..."
		}
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\n", job.ID, job.Status, job.Backend, job.Mode, job.RequestID, text); err != nil {
			return err
		}
	}
	return nil
}

func RetryJob(ctx context.Context, database *store.Store, jobID int64) error {
	return database.RetryJob(ctx, jobID)
}
