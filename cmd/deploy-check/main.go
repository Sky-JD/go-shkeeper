package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Sky-JD/go-shkeeper/internal/deploycheck"
)

func main() {
	startedAt := time.Now()
	reportFile := strings.TrimSpace(os.Getenv("DEPLOY_CHECK_REPORT_FILE"))
	cfg, err := deploycheck.LoadConfig()
	if err != nil {
		writeConfigFailureReport(reportFile, startedAt, err)
		fmt.Fprintf(os.Stderr, "deploy-check config failed: %v\n", err)
		os.Exit(1)
	}
	if strings.TrimSpace(cfg.ReportFile) != "" {
		reportFile = strings.TrimSpace(cfg.ReportFile)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	runner := deploycheck.NewRunner(cfg, os.Stdout)
	err = runner.Run(ctx)
	if reportFile != "" {
		if reportErr := runner.WriteReport(reportFile, err); reportErr != nil {
			fmt.Fprintf(os.Stderr, "deploy-check report failed: %v\n", reportErr)
			if err == nil {
				os.Exit(1)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "deploy-check failed: %v\n", err)
		os.Exit(1)
	}
}

func writeConfigFailureReport(path string, startedAt time.Time, err error) {
	if path == "" {
		return
	}
	finishedAt := time.Now()
	report := deploycheck.Report{
		Status:     "failed",
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
		DurationMS: finishedAt.Sub(startedAt).Milliseconds(),
		Error:      err.Error(),
		Checks: []deploycheck.ReportCheck{{
			Name:      "config",
			Status:    "failed",
			Error:     err.Error(),
			Timestamp: finishedAt,
		}},
	}
	if reportErr := deploycheck.WriteReportFile(path, report); reportErr != nil {
		fmt.Fprintf(os.Stderr, "deploy-check report failed: %v\n", reportErr)
	}
}
