package main

import (
	"fmt"
	"os"

	"github.com/Sky-JD/go-shkeeper/internal/releaseaudit"
)

func main() {
	cfg := releaseaudit.LoadConfigFromEnv()
	report, err := releaseaudit.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "release-audit failed: %v\n", err)
		os.Exit(1)
	}
	if err := releaseaudit.WriteReport(cfg.OutputFile, report); err != nil {
		fmt.Fprintf(os.Stderr, "release-audit write failed: %v\n", err)
		os.Exit(1)
	}
	if err := releaseaudit.ErrorIfFailed(report); err != nil {
		_ = releaseaudit.WriteReport(cfg.OutputFile, report)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("release_audit_ok coverage_cryptos=%d\n", len(report.CoverageCryptos))
}
