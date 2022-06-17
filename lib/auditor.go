package lib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/google/go-github/v45/github"
	"github.com/hmarr/codeowners"
)

type CodeownersReport struct {
	Missing []github.Repository
	Invalid []github.Repository
	Present []github.Repository
}

func NewAuditor(g *github.Client) *Auditor {
	return &Auditor{
		ghClient: g,
	}
}

type Auditor struct {
	ghClient *github.Client
}

func (a *Auditor) CheckForCodeownersFiles(ctx context.Context, organization string) (CodeownersReport, error) {
	c := CodeownersReport{}
	errBus := make(chan error)
	defer close(errBus)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultStream := buildReport(ctx, a.validateCodeowners(ctx, a.fetchCodeowners(ctx, a.scanRepositories(ctx, organization, errBus))))

	for {
		select {
		case <-ctx.Done():
			return c, errors.New("cancelled operation")
		case err := <-errBus:
			return c, fmt.Errorf("failed to scan: %w", err)
		case c, ok := <-resultStream:
			if !ok {
				return c, errors.New("unexpected halt in scan")
			}
			return c, nil
		}
	}
}

func (a *Auditor) scanRepositories(ctx context.Context, organization string, errBus chan<- error) <-chan github.Repository {
	valStream := make(chan github.Repository)

	go func() {
		defer close(valStream)
		opts := &github.RepositoryListByOrgOptions{}

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			repos, resp, err := a.ghClient.Repositories.ListByOrg(ctx, organization, opts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to list repos: %s", err)
				errBus <- err
				return
			}

			for _, repo := range repos {
				valStream <- *repo
			}

			if resp.NextPage == 0 {
				return
			}

			opts.Page = resp.NextPage
		}
	}()

	return valStream
}

type codeownersFetch struct {
	Repo     github.Repository
	Contents *github.RepositoryContent
	IsValid  bool
	Err      error
}

const (
	codeownersPath = "/CODEOWNERS"
)

func (a *Auditor) fetchCodeowners(ctx context.Context, input <-chan github.Repository) <-chan codeownersFetch {
	valStream := make(chan codeownersFetch)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case r, ok := <-input:
					if !ok {
						return
					}
					fc, _, _, err := a.ghClient.Repositories.GetContents(
						ctx, *r.Owner.Name, *r.Name, codeownersPath, &github.RepositoryContentGetOptions{})
					valStream <- codeownersFetch{
						Repo:     r,
						Contents: fc,
						Err:      err,
					}
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(valStream)
	}()

	return valStream
}

func (a *Auditor) validateCodeowners(ctx context.Context, input <-chan codeownersFetch) <-chan codeownersFetch {
	valStream := make(chan codeownersFetch)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer close(valStream)
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-input:
				if !ok {
					return
				}
				if f.Err != nil {
					valStream <- f
					continue
				}

				raw, err := f.Contents.GetContent()
				if err != nil {
					f.Err = err
					valStream <- f
					continue
				}

				r := strings.NewReader(raw)
				parsed, err := codeowners.ParseFile(r)
				if err != nil {
					valStream <- f
					continue
				}

				f.IsValid = a.codeownersExist(ctx, parsed)
				valStream <- f
			}
		}
	}()
	return valStream
}

func (a *Auditor) codeownersExist(ctx context.Context, rules codeowners.Ruleset) bool {
	teams := make(map[string]int)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, rule := range rules {
		for _, owner := range rule.Owners {
			if owner.Type == "team" {
				teams[owner.Value]++
			}
		}
	}

	for teamSlug := range teams {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		parts := strings.Split(teamSlug, "/")[:2]
		org, teamName := parts[0], parts[1]
		org = strings.TrimPrefix(org, "@")
		_, _, err := a.ghClient.Teams.GetTeamBySlug(ctx, org, teamName)
		if err != nil {
			return false
		}
	}
	return true
}

func buildReport(ctx context.Context, input <-chan codeownersFetch) <-chan CodeownersReport {
	valStream := make(chan CodeownersReport)

	go func() {
		defer close(valStream)

		r := CodeownersReport{
			Missing: make([]github.Repository, 0),
			Present: make([]github.Repository, 0),
		}
		for {
			select {
			case <-ctx.Done():
				return
			case f, ok := <-input:
				if !ok {
					valStream <- r
					return
				}
				if f.Err != nil {
					r.Missing = append(r.Missing, f.Repo)
					continue
				}
				if !f.IsValid {
					r.Missing = append(r.Missing, f.Repo)
					continue
				}
				r.Present = append(r.Present, f.Repo)
			}
		}
	}()

	return valStream
}
