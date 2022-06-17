package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"time"

	"github.com/dishbreak/codeowners-audit/lib"
	"github.com/dishbreak/codeowners-audit/utils"
	"github.com/google/go-github/v45/github"
)

func main() {
	if len(os.Args) == 1 || len(os.Args) > 2 {
		fmt.Fprintf(os.Stderr, "need exactly one argument!\n")
		os.Exit(1)
	}
	org := os.Args[1]

	token, ok := os.LookupEnv("GITHUB_TOKEN")
	if !ok {
		fmt.Fprintf(os.Stderr, "need Github Token set in GITHUB_TOKEN environment variable\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	g := utils.GithubClientWithToken(ctx, token)

	a := lib.NewAuditor(g)
	results, err := a.CheckForCodeownersFiles(ctx, org)
	if err != nil {
		panic(err)
	}

	totalCount := len(results.Invalid) + len(results.Missing) + len(results.Present)
	noun := "repositories"
	if totalCount == 1 {
		noun = "repository"
	}

	fmt.Printf("%d %s scanned: %d with no CODEOWNERS, %d with invalid CODEOWNERS",
		totalCount, noun, len(results.Missing), len(results.Invalid))

	filename := fmt.Sprintf("audit-%d.csv", time.Now().Unix())

	f, err := os.Create(filename)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING! Failed to write results to %s", filename)
		os.Exit(2)
	}
	defer f.Close()

	c := csv.NewWriter(f)
	c.Write([]string{"repo", "url", "status", "last_pushed"})
	for status, repos := range map[string][]github.Repository{
		"compliant":    results.Present,
		"missing_file": results.Missing,
		"invalid_file": results.Invalid,
	} {
		for _, val := range repos {
			c.Write([]string{*val.Name, *val.ContentsURL, status, val.PushedAt.Format(time.RFC3339)})
		}
	}

	fmt.Printf("wrote results to %s\n", filename)
}
