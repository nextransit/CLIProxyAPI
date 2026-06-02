package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	management "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
)

func main() {
	nowFlag := flag.String("now", "", "Evaluation current time in RFC3339 format")
	flag.Parse()

	var now time.Time
	if *nowFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid -now value: %v\n", err)
			os.Exit(1)
		}
		now = parsed.UTC()
	} else {
		now = time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	}

	report := management.EvaluateTextOpsHeuristicAccuracy(now)
	output, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(output))
}
