// Package cli contains the command interface and bounded operation scheduler.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/yongsan/yongsan-at-your-service/internal/config"
	"github.com/yongsan/yongsan-at-your-service/internal/gitlab"
)

// Options allows callers and tests to supply streams and execution settings.
type Options struct {
	In          io.Reader
	Out         io.Writer
	Err         io.Writer
	Interactive bool
	Version     string

	// ConfigPath lets embedded callers supply a configuration location. The
	// executable leaves it empty to use yas.yaml beside its resolved binary.
	ConfigPath string
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string   { return e.err.Error() }
func (e *exitError) Unwrap() error   { return e.err }
func usageError(err error) error     { return &exitError{code: 2, err: err} }
func operationError(err error) error { return &exitError{code: 1, err: err} }

// ExitCode distinguishes invalid input/configuration from operation failures.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 2 // Includes Cobra's unknown commands and argument errors.
}

type app struct {
	options        Options
	prompt         *prompter
	gitlabURL      string
	tokenEnv       string
	timeout        string
	format         string
	dryRun         bool
	nonInteractive bool
	noProgress     bool
	logMu          sync.Mutex
	buildTargets   map[string]bool
}

// New creates an independent command tree. By default, configuration lives
// beside the resolved yas executable.
func New(options Options) *cobra.Command {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	a := &app{options: options}
	a.prompt = &prompter{
		reader: bufio.NewReader(options.In), out: options.Err,
		interactive: func() bool { return options.Interactive && !a.nonInteractive },
	}
	root := &cobra.Command{
		Use: "yas", Short: "Concurrent GitLab tagging, Docker builds, and release scans",
		Long: "Tag GitLab repositories, build Docker images, and collect release commits from exact revisions.\nConfiguration is stored in yas.yaml beside the resolved yas executable.",
		Args: cobra.NoArgs, SilenceErrors: true, SilenceUsage: true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.SetIn(options.In)
	root.SetOut(options.Out)
	root.SetErr(options.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
	f := root.PersistentFlags()
	f.StringVar(&a.gitlabURL, "gitlab-url", "", "Override the configured GitLab server URL")
	f.StringVar(&a.tokenEnv, "token-env", "", "Override the environment variable containing the GitLab token")
	f.StringVar(&a.timeout, "timeout", "", "Time limit for each repository job (for example 2m or 30m)")
	f.StringVar(&a.format, "format", "text", "Result format: text or json")
	f.BoolVar(&a.dryRun, "dry-run", false, "Show planned work without creating/deleting tags or building images")
	f.BoolVar(&a.nonInteractive, "non-interactive", false, "Fail on missing required inputs instead of prompting")
	f.BoolVar(&a.noProgress, "no-progress", false, "Hide live operation progress (Docker logs and final results remain)")

	tag := &cobra.Command{Use: "tag", Short: "Create or delete GitLab repository tags", Args: cobra.NoArgs}
	build := &cobra.Command{Use: "build", Short: "Build Docker images from GitLab source archives", Args: cobra.NoArgs}
	tagOne, tagBatch := a.tagOne(), a.tagBatch()
	buildOne, buildBatch := a.buildOne(), a.buildBatch()
	deleteOne, deleteBatch := a.deleteTag(false), a.deleteTag(true)
	releaseScan, releaseRender := a.releaseScan(), a.releaseRender()
	releaseCmd := &cobra.Command{Use: "release", Short: "Scan release commits and generate HTML release notes", Args: cobra.NoArgs}
	releaseCmd.AddCommand(releaseScan, releaseRender)
	deleteCmd := &cobra.Command{Use: "delete", Short: "Delete a named Git tag", Args: cobra.NoArgs}
	deleteCmd.AddCommand(deleteOne, deleteBatch)
	tag.AddCommand(tagOne, tagBatch, deleteCmd)
	build.AddCommand(buildOne, buildBatch)
	root.AddCommand(tag, build, releaseCmd, a.configCommand())
	root.AddCommand(&cobra.Command{
		Use: "version", Short: "Print the application version", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(options.Out, "yas", options.Version)
			return err
		},
	})
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if !a.prompt.interactive() {
			return cmd.Help()
		}
		index, err := a.prompt.choose(cmd.Context(), "Choose an operation:", []string{
			"Tag one repository", "Tag repositories from config", "Build one Docker image", "Build Docker images from config",
			"Delete a tag from one repository", "Delete a tag from tagging repositories",
			"Scan release changes into YAML", "Generate HTML release notes",
		})
		if err != nil {
			return usageError(err)
		}
		selected := []*cobra.Command{tagOne, tagBatch, buildOne, buildBatch, deleteOne, deleteBatch, releaseScan, releaseRender}[index]
		selected.SetContext(cmd.Context())
		return selected.RunE(selected, nil)
	}
	return root
}

func changed(cmd *cobra.Command, name string) bool {
	for current := cmd; current != nil; current = current.Parent() {
		if f := current.Flags().Lookup(name); f != nil && f.Changed {
			return true
		}
		if f := current.PersistentFlags().Lookup(name); f != nil && f.Changed {
			return true
		}
	}
	return false
}

func (a *app) configPath() (string, error) {
	if a.options.ConfigPath != "" {
		return filepath.Abs(a.options.ConfigPath)
	}
	return config.Path()
}

func (a *app) loadConfig() (*config.Config, error) {
	filename, err := a.configPath()
	if err != nil {
		return nil, err
	}
	return config.LoadFile(filename)
}

func (a *app) load(cmd *cobra.Command, operation string, batch bool) (*config.Config, error) {
	if a.format != "text" && a.format != "json" {
		return nil, usageError(fmt.Errorf("--format must be text or json"))
	}
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, usageError(err)
	}
	if changed(cmd, "gitlab-url") {
		cfg.GitLab.URL = a.gitlabURL
	}
	if changed(cmd, "token-env") {
		cfg.GitLab.TokenEnv = a.tokenEnv
		cfg.GitLab.Token = "" // An explicit flag selects environment authentication.
	}
	if err := cfg.ValidateCommon(); err != nil {
		return nil, usageError(err)
	}
	if operation == "tag" {
		if changed(cmd, "timeout") {
			cfg.Tagging.Timeout = a.timeout
		}
	}
	if operation == "build" {
		if changed(cmd, "timeout") {
			cfg.Building.Timeout = a.timeout
		}
	}
	return cfg, nil
}

func clientFor(cfg *config.Config) (*gitlab.Client, error) {
	token := strings.TrimSpace(cfg.GitLab.Token)
	if token == "" {
		token = os.Getenv(cfg.GitLab.TokenEnv)
	}
	if strings.TrimSpace(token) == "" {
		return nil, usageError(fmt.Errorf("set gitlab.token in yas.yaml or set the %s environment variable to a GitLab access token", cfg.GitLab.TokenEnv))
	}
	c, err := gitlab.NewWithOptions(cfg.GitLab.URL, token, gitlab.Options{
		InsecureSkipVerify: cfg.GitLab.InsecureSkipVerify,
	})
	if err != nil {
		return nil, usageError(err)
	}
	return c, nil
}

func (a *app) tagOne() *cobra.Command {
	var repo config.TagRepository
	cmd := &cobra.Command{Use: "one", Short: "Tag one repository; prompt for missing required inputs", Args: cobra.NoArgs}
	f := cmd.Flags()
	f.StringVar(&repo.Project, "project", "", "GitLab project ID or full namespace/project path")
	f.StringVar(&repo.Branch, "branch", "", "Source branch; validates reachability when --commit is also supplied")
	f.StringVar(&repo.Commit, "commit", "", "Source commit SHA (full or at least 7 characters)")
	f.StringVar(&repo.Tag, "tag", "", "Destination Git tag name")
	f.StringVar(&repo.Message, "message", "", "Annotation for a new tag")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load(cmd, "tag", false)
		if err != nil {
			return err
		}
		settings := *cfg
		settings.Tagging.Repositories = nil
		settings.Tagging.Defaults.Tag = "" // Single commands require their own destination tag.
		if err := settings.ValidateTagging(); err != nil {
			return usageError(err)
		}
		if !changed(cmd, "message") {
			repo.Message = cfg.Tagging.Defaults.Message
		}
		if err := a.prompt.project(cmd.Context(), cfg.GitLab.Groups, &repo.Project); err != nil {
			return usageError(err)
		}
		if repo.Commit == "" && repo.Branch == "" {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("supply --branch or --commit"))
			}
			choice, err := a.prompt.choose(cmd.Context(), "Select tag source:", []string{"Branch", "Commit", "Commit with branch validation"})
			if err != nil {
				return usageError(err)
			}
			if choice == 0 || choice == 2 {
				if err := a.prompt.required(cmd.Context(), "branch", "Branch", &repo.Branch, config.ValidateGitRef); err != nil {
					return usageError(err)
				}
			}
			if choice == 1 || choice == 2 {
				if err := a.prompt.required(cmd.Context(), "commit", "Commit SHA (full or at least 7 characters)", &repo.Commit, config.ValidateCommit); err != nil {
					return usageError(err)
				}
			}
		}
		if err := a.prompt.required(cmd.Context(), "tag", "New Git tag name", &repo.Tag, config.ValidateGitRef); err != nil {
			return usageError(err)
		}
		if err := config.ValidateTag(repo); err != nil {
			return usageError(err)
		}
		c, err := clientFor(cfg)
		if err != nil {
			return err
		}
		timeout, _ := time.ParseDuration(cfg.Tagging.Timeout)
		return a.execute(cmd.Context(), []job{a.tagJob(c, repo)}, 1, timeout)
	}
	return cmd
}

func (a *app) tagBatch() *cobra.Command {
	var report batchReportOptions
	var scope groupOptions
	var tag, message, branch string
	var only []string
	var concurrency int
	cmd := &cobra.Command{Use: "batch", Short: "Tag repositories from tagging.repositories in yas.yaml", Args: cobra.NoArgs}
	report.bind(cmd)
	scope.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&branch, "branch", "", "Source branch for every selected repository (required; prompted if missing)")
	f.StringVar(&tag, "tag", "", "Override the destination tag for every selected repository")
	f.StringVar(&message, "message", "", "Override the tag annotation for every selected repository")
	f.StringArrayVar(&only, "only", nil, "Select a configured project identifier (repeatable)")
	f.IntVar(&concurrency, "concurrency", 0, "Maximum simultaneous repository jobs")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := report.validate(cmd); err != nil {
			return usageError(err)
		}
		cfg, err := a.load(cmd, "tag", true)
		if err != nil {
			return err
		}
		if changed(cmd, "concurrency") {
			cfg.Tagging.Concurrency = concurrency
		}
		if changed(cmd, "tag") {
			cfg.Tagging.Defaults.Tag = tag
		}
		if changed(cmd, "message") {
			cfg.Tagging.Defaults.Message = message
		}
		projects := make([]string, len(cfg.Tagging.Repositories))
		for i, r := range cfg.Tagging.Repositories {
			projects[i] = r.Project
		}
		indices, err := a.selectBatch(cmd, projects, only, scope)
		if err != nil {
			return usageError(err)
		}
		if err := a.prompt.required(cmd.Context(), "branch", "Source branch for all selected repositories", &branch, config.ValidateGitRef); err != nil {
			return usageError(err)
		}
		needsTag := false
		for _, i := range indices {
			if config.ApplyTagDefaults(cfg.Tagging.Repositories[i], cfg.Tagging.Defaults).Tag == "" {
				needsTag = true
			}
		}
		if needsTag && !changed(cmd, "tag") {
			if err := a.prompt.required(cmd.Context(), "tag", "New Git tag name for all selected repositories", &tag, config.ValidateGitRef); err != nil {
				return usageError(err)
			}
		}
		repos := make([]config.TagRepository, 0, len(indices))
		for _, i := range indices {
			r := config.ApplyTagDefaults(cfg.Tagging.Repositories[i], cfg.Tagging.Defaults)
			r.Branch = branch
			if changed(cmd, "tag") || needsTag {
				r.Tag = tag
			}
			if changed(cmd, "message") {
				r.Message = message
			}
			if err := config.ValidateTag(r); err != nil {
				return usageError(err)
			}
			repos = append(repos, r)
		}
		cfg.Tagging.Repositories = repos
		// Validate resolved destinations so unused defaults cannot defeat overrides.
		cfg.Tagging.Defaults.Tag = ""
		if err := cfg.ValidateTagging(); err != nil {
			return usageError(err)
		}
		c, err := clientFor(cfg)
		if err != nil {
			return err
		}
		jobs := make([]job, len(repos))
		for i, r := range repos {
			jobs[i] = a.tagJob(c, r)
		}
		timeout, _ := time.ParseDuration(cfg.Tagging.Timeout)
		return a.executeBatch(cmd.Context(), jobs, cfg.Tagging.Concurrency, timeout, report, "tag", cfg.GitLab.URL)
	}
	return cmd
}

type buildFlags struct {
	context, dockerfile string
	buildArgs           []string
	noCache, pull       bool
}

func (b *buildFlags) bind(cmd *cobra.Command) {
	f := cmd.Flags()
	f.StringVar(&b.context, "context", "", "Build context directory relative to the repository root")
	f.StringVar(&b.dockerfile, "dockerfile", "", "Dockerfile path relative to the build context")
	f.StringArrayVar(&b.buildArgs, "build-arg", nil, "Build argument KEY=VALUE (repeatable)")
	f.BoolVar(&b.noCache, "no-cache", false, "Disable Docker build-cache reuse")
	f.BoolVar(&b.pull, "pull", false, "Refresh referenced base images")
}

func (b *buildFlags) apply(cmd *cobra.Command, base config.BuildOptions) (config.BuildOptions, error) {
	var override config.BuildOptions
	if changed(cmd, "context") {
		if b.context == "" {
			return base, fmt.Errorf("--context cannot be empty")
		}
		override.Context = b.context
	}
	if changed(cmd, "dockerfile") {
		if b.dockerfile == "" {
			return base, fmt.Errorf("--dockerfile cannot be empty")
		}
		override.Dockerfile = b.dockerfile
	}
	if changed(cmd, "no-cache") {
		override.NoCache = &b.noCache
	}
	if changed(cmd, "pull") {
		override.Pull = &b.pull
	}
	if len(b.buildArgs) > 0 {
		override.BuildArgs = make(map[string]string)
		for _, argument := range b.buildArgs {
			key, value, ok := strings.Cut(argument, "=")
			if !ok || key == "" {
				return base, fmt.Errorf("invalid --build-arg %q: expected KEY=VALUE", argument)
			}
			override.BuildArgs[key] = value
		}
	}
	return config.MergeBuildOptions(base, override), nil
}

func (a *app) buildOne() *cobra.Command {
	var save imageSaveOptions
	var prefixFlags imagePrefixFlags
	var repo config.BuildRepository
	var flags buildFlags
	cmd := &cobra.Command{Use: "one", Short: "Build one image; prompt for missing required inputs", Args: cobra.NoArgs}
	save.bind(cmd)
	f := cmd.Flags()
	f.StringVar(&repo.Project, "project", "", "GitLab project ID or full namespace/project path")
	f.StringVar(&repo.Source.Commit, "commit", "", "Source commit SHA (full or at least 7 characters) (exclusive with --tag)")
	f.StringVar(&repo.Source.Tag, "tag", "", "Source Git tag (exclusive with --commit)")
	f.StringVar(&repo.Image, "image", "", "Override output image (default: repository name and source Git tag/resolved commit SHA)")
	flags.bind(cmd)
	prefixFlags.bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cfg, err := a.load(cmd, "build", false)
		if err != nil {
			return err
		}
		repo.BuildOptions, err = flags.apply(cmd, cfg.Building.Defaults)
		if err != nil {
			return usageError(err)
		}
		settings := *cfg
		settings.Building.Repositories = nil
		settings.Building.Defaults = repo.BuildOptions
		if err := settings.ValidateBuilding(); err != nil {
			return usageError(err)
		}
		if repo.Source.Commit != "" && repo.Source.Tag != "" {
			return usageError(fmt.Errorf("--commit and --tag are mutually exclusive for builds"))
		}
		if err := a.prompt.project(cmd.Context(), cfg.GitLab.Groups, &repo.Project); err != nil {
			return usageError(err)
		}
		if repo.Source.Commit == "" && repo.Source.Tag == "" {
			if !a.prompt.interactive() {
				return usageError(fmt.Errorf("supply exactly one of --commit or --tag"))
			}
			choice, err := a.prompt.choose(cmd.Context(), "Select build source:", []string{"Git tag", "Commit"})
			if err != nil {
				return usageError(err)
			}
			if choice == 0 {
				if err := a.prompt.required(cmd.Context(), "tag", "Source Git tag", &repo.Source.Tag, config.ValidateGitRef); err != nil {
					return usageError(err)
				}
			} else {
				if err := a.prompt.required(cmd.Context(), "commit", "Commit SHA (full or at least 7 characters)", &repo.Source.Commit, config.ValidateCommit); err != nil {
					return usageError(err)
				}
			}
		}
		prefix, err := a.chooseImagePrefix(cmd, cfg.Building.ImagePrefixes, prefixFlags, repo.Image == "")
		if err != nil {
			return usageError(err)
		}
		if err := a.imageOverride(cmd, &repo.Image, prefix, repo.Project); err != nil {
			return usageError(err)
		}
		if err := a.buildContext(cmd, &repo.BuildOptions); err != nil {
			return usageError(err)
		}
		if repo.Image != "" {
			repo.Image, err = prefixedImage(prefix, repo.Image)
			if err != nil {
				return usageError(err)
			}
		} else {
			repo.ImagePrefix = prefix
		}
		if err := config.ValidateBuild(repo); err != nil {
			return usageError(err)
		}
		saveOptions, err := a.chooseImageSave(cmd, save)
		if err != nil {
			return usageError(err)
		}
		c, err := clientFor(cfg)
		if err != nil {
			return err
		}
		timeout, _ := time.ParseDuration(cfg.Building.Timeout)
		return a.execute(saveOptions.context(cmd.Context()), []job{a.buildJob(c, repo, saveOptions)}, 1, timeout)
	}
	return cmd
}

func (a *app) buildBatch() *cobra.Command {
	var save imageSaveOptions
	var report batchReportOptions
	var tag string
	var images []string
	var prefixFlags imagePrefixFlags
	var scope groupOptions
	var flags buildFlags
	var concurrency int
	var only []string
	cmd := &cobra.Command{Use: "batch", Short: "Build images from building.repositories in yas.yaml", Args: cobra.NoArgs}
	save.bind(cmd)
	report.bind(cmd)
	scope.bind(cmd)
	f := cmd.Flags()
	f.StringArrayVar(&images, "image", nil, "Override one selected image: PROJECT=NAME:TAG (repeatable)")
	f.StringVar(&tag, "tag", "", "Source Git tag for every selected repository (required; prompted if missing)")
	f.IntVar(&concurrency, "concurrency", 0, "Maximum simultaneous repository jobs")
	f.StringArrayVar(&only, "only", nil, "Select a configured project identifier (repeatable)")
	flags.bind(cmd)
	prefixFlags.bind(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := report.validate(cmd); err != nil {
			return usageError(err)
		}
		cfg, err := a.load(cmd, "build", true)
		if err != nil {
			return err
		}
		if changed(cmd, "concurrency") {
			cfg.Building.Concurrency = concurrency
		}
		cfg.Building.Defaults, err = flags.apply(cmd, cfg.Building.Defaults)
		if err != nil {
			return usageError(err)
		}
		projects := make([]string, len(cfg.Building.Repositories))
		for i, r := range cfg.Building.Repositories {
			projects[i] = r.Project
		}
		indices, err := a.selectBatch(cmd, projects, only, scope)
		if err != nil {
			return usageError(err)
		}
		if err := a.prompt.required(cmd.Context(), "tag", "Source Git tag for all selected repositories", &tag, config.ValidateGitRef); err != nil {
			return usageError(err)
		}
		prefix, err := a.chooseImagePrefix(cmd, cfg.Building.ImagePrefixes, prefixFlags, true)
		if err != nil {
			return usageError(err)
		}
		selectedProjects := make([]string, 0, len(indices))
		for _, i := range indices {
			selectedProjects = append(selectedProjects, projects[i])
		}
		imageOverrides, err := batchImageOverrides(images, selectedProjects)
		if err != nil {
			return usageError(err)
		}
		if err := a.buildContext(cmd, &cfg.Building.Defaults); err != nil {
			return usageError(err)
		}
		repos := make([]config.BuildRepository, 0, len(indices))
		for _, i := range indices {
			r := cfg.Building.Repositories[i]
			r.Source = config.Source{Tag: tag}
			// Batch names are automatic unless explicitly overridden by a flag.
			// Never prompt once per repository or inherit legacy config images.
			r.Image = imageOverrides[r.Project]
			if r.Image != "" {
				r.Image, err = prefixedImage(prefix, r.Image)
				if err != nil {
					return usageError(err)
				}
			} else {
				r.ImagePrefix = prefix
			}
			r.BuildOptions = config.MergeBuildOptions(cfg.Building.Defaults, r.BuildOptions)
			if a.prompt.interactive() && !changed(cmd, "context") {
				r.Context = cfg.Building.Defaults.Context
			}
			r.BuildOptions, err = flags.apply(cmd, r.BuildOptions)
			if err != nil {
				return usageError(err)
			}
			if err := config.ValidateBuild(r); err != nil {
				return usageError(err)
			}
			repos = append(repos, r)
		}
		cfg.Building.Repositories = repos
		// Each entry now contains its effective options.
		cfg.Building.Defaults = config.BuildOptions{Context: ".", Dockerfile: "Dockerfile"}
		if err := cfg.ValidateBuilding(); err != nil {
			return usageError(err)
		}
		saveOptions, err := a.chooseImageSave(cmd, save)
		if err != nil {
			return usageError(err)
		}
		c, err := clientFor(cfg)
		if err != nil {
			return err
		}
		jobs := make([]job, len(repos))
		for i, r := range repos {
			jobs[i] = a.buildJob(c, r, saveOptions)
		}
		timeout, _ := time.ParseDuration(cfg.Building.Timeout)
		return a.executeBatch(saveOptions.context(cmd.Context()), jobs, cfg.Building.Concurrency, timeout, report, "build", cfg.GitLab.URL)
	}
	return cmd
}

func selectProjects(projects, only []string) ([]int, error) {
	if len(projects) == 0 {
		return nil, fmt.Errorf("the operation's repository list in yas.yaml is empty")
	}
	wanted := make(map[string]bool, len(only))
	for _, p := range only {
		wanted[p] = false
	}
	var indices []int
	for i, p := range projects {
		if len(only) == 0 {
			indices = append(indices, i)
			continue
		}
		if _, ok := wanted[p]; ok {
			indices = append(indices, i)
			wanted[p] = true
		}
	}
	for _, p := range only {
		if !wanted[p] {
			return nil, fmt.Errorf("--only project %q is not in this operation's repository list", p)
		}
	}
	return indices, nil
}

func (a *app) configCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Create or validate yas.yaml beside the executable", Args: cobra.NoArgs}
	cmd.AddCommand(&cobra.Command{
		Use: "init", Short: "Create yas.yaml beside the executable without overwriting an existing file", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			filename, err := a.configPath()
			if err != nil {
				return usageError(err)
			}
			file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if errors.Is(err, os.ErrExist) {
				return usageError(fmt.Errorf("%s already exists; it was not overwritten", filename))
			}
			if err != nil {
				return operationError(fmt.Errorf("create configuration beside the executable at %s: %w", filename, err))
			}
			_, writeErr := io.WriteString(file, config.Example)
			closeErr := file.Close()
			if writeErr != nil {
				return operationError(writeErr)
			}
			if closeErr != nil {
				return operationError(closeErr)
			}
			_, err = fmt.Fprintf(a.options.Out, "Created %s. Edit its projects, sources, and images before running operations.\n", filename)
			return err
		},
	}, &cobra.Command{
		Use: "validate", Short: "Validate yas.yaml beside the executable without contacting GitLab or Docker", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return usageError(err)
			}
			if err := cfg.Validate(); err != nil {
				return usageError(err)
			}
			_, err = fmt.Fprintln(a.options.Out, "yas.yaml is valid.")
			return err
		},
	})
	return cmd
}
