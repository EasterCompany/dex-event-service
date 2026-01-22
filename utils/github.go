package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type GitHubIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Repo   string `json:"repository"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func getWorkingDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "EasterCompany")
}

func ListGitHubIssues() ([]GitHubIssue, error) {
	cmd := exec.Command("gh", "issue", "list", "--repo", "EasterCompany/EasterCompany", "--state", "open", "--json", "number,title,body,state,labels,createdAt,updatedAt,repository", "--limit", "100")
	cmd.Dir = getWorkingDir()
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list github issues: %w (output: %s)", err, string(out))
	}

	// Internal GH JSON might have repository as an object
	type ghRepo struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	}
	type ghIssueInternal struct {
		Number     int    `json:"number"`
		Title      string `json:"title"`
		Body       string `json:"body"`
		State      string `json:"state"`
		Repository ghRepo `json:"repository"`
		Labels     []struct {
			Name string `json:"name"`
		} `json:"labels"`
		CreatedAt string `json:"createdAt"`
		UpdatedAt string `json:"updatedAt"`
	}

	var internalIssues []ghIssueInternal
	if err := json.Unmarshal(out, &internalIssues); err != nil {
		return nil, fmt.Errorf("failed to parse github issues: %w", err)
	}

	issues := make([]GitHubIssue, len(internalIssues))
	for i, internal := range internalIssues {
		issues[i] = GitHubIssue{
			Number:    internal.Number,
			Title:     internal.Title,
			Body:      internal.Body,
			State:     internal.State,
			Repo:      internal.Repository.NameWithOwner,
			Labels:    internal.Labels,
			CreatedAt: internal.CreatedAt,
			UpdatedAt: internal.UpdatedAt,
		}
	}

	return issues, nil
}

func CreateGitHubIssue(title, body string) (int, error) {
	cmd := exec.Command("gh", "issue", "create", "--repo", "EasterCompany/EasterCompany", "--title", title, "--body", body, "--label", "roadmap")
	cmd.Dir = getWorkingDir()
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("failed to create github issue: %w (output: %s)", err, string(out))
	}

	// Output is usually something like "https://github.com/EasterCompany/EasterCompany/issues/123"
	var number int
	_, _ = fmt.Sscanf(filepath.Base(string(out)), "%d", &number)

	return number, nil
}

func CloseGitHubIssue(number int) error {
	cmd := exec.Command("gh", "issue", "close", fmt.Sprintf("%d", number), "--repo", "EasterCompany/EasterCompany")
	cmd.Dir = getWorkingDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to close github issue: %w (output: %s)", err, string(out))
	}
	return nil
}

func AddGitHubIssueComment(number int, body string) error {
	cmd := exec.Command("gh", "issue", "comment", fmt.Sprintf("%d", number), "--repo", "EasterCompany/EasterCompany", "--body", body)
	cmd.Dir = getWorkingDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add github issue comment: %w (output: %s)", err, string(out))
	}
	return nil
}

func GetGitHubIssueCount() (int, error) {
	// Simple count of open issues
	issues, err := ListGitHubIssues()
	if err != nil {
		return 0, err
	}
	return len(issues), nil
}
