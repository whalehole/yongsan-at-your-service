// Package config loads and validates the fixed yas.yaml configuration file.
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const Filename = "yas.yaml"

// Example is the starter configuration written by yas config init.
//
//go:embed example.yaml
var Example string

type Config struct {
	Version          int            `yaml:"version"`
	WorkingDirectory string         `yaml:"working_directory"`
	GitLab           GitLabConfig   `yaml:"gitlab"`
	Tagging          TaggingConfig  `yaml:"tagging"`
	Building         BuildingConfig `yaml:"building"`
	Release          ReleaseConfig  `yaml:"release"`
}

type GitLabConfig struct {
	URL                string   `yaml:"url"`
	Token              string   `yaml:"token"`
	TokenEnv           string   `yaml:"token_env"`
	Groups             []string `yaml:"groups"`
	InsecureSkipVerify bool     `yaml:"insecure_skip_verify"`
}

type TaggingConfig struct {
	Concurrency  int             `yaml:"concurrency"`
	Timeout      string          `yaml:"timeout"`
	Defaults     TagDefaults     `yaml:"defaults"`
	Repositories []TagRepository `yaml:"repositories"`
}

type TagDefaults struct {
	Tag     string `yaml:"tag"`
	Message string `yaml:"message"`
}

type TagRepository struct {
	Project string `yaml:"project"`
	Branch  string `yaml:"-"` // Runtime input; batch branches are selected per invocation.
	Commit  string `yaml:"commit"`
	Tag     string `yaml:"tag"`
	Message string `yaml:"message"`
}

type ReleaseConfig struct {
	Concurrency  int                 `yaml:"concurrency"`
	Timeout      string              `yaml:"timeout"`
	Repositories []ReleaseRepository `yaml:"repositories"`
}

type ReleaseRepository struct {
	Project string `yaml:"project"`
}

type BuildingConfig struct {
	ImagePrefixes []string          `yaml:"image_prefixes"`
	Concurrency   int               `yaml:"concurrency"`
	Timeout       string            `yaml:"timeout"`
	Defaults      BuildOptions      `yaml:"defaults"`
	Repositories  []BuildRepository `yaml:"repositories"`
}

// BuildOptions uses pointers for Boolean settings so explicit false values can
// override true values inherited from defaults.
type BuildOptions struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	NoCache    *bool             `yaml:"no_cache"`
	Pull       *bool             `yaml:"pull"`
	BuildArgs  map[string]string `yaml:"build_args"`
}

type BuildRepository struct {
	Project      string `yaml:"project"`
	Source       Source `yaml:"source"`
	Image        string `yaml:"image"`
	ImagePrefix  string `yaml:"-"` // Runtime prefix for an automatically named image.
	BuildOptions `yaml:",inline"`
}

type Source struct {
	Commit string `yaml:"commit"`
	Tag    string `yaml:"tag"`
}

// Path locates yas.yaml beside the real executable, resolving invocation
// symlinks rather than using the working directory or symlink directory.
func Path() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable for configuration: %w", err)
	}
	return pathForExecutable(executable)
}

func pathForExecutable(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("resolve executable path %q for configuration: %w", executable, err)
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve absolute executable path %q for configuration: %w", resolved, err)
	}
	return filepath.Join(filepath.Dir(absolute), Filename), nil
}

// Load resolves the executable-side locator, then reads the operational config.
func Load() (*Config, error) {
	location, err := ResolveLocation("")
	if err != nil {
		return nil, err
	}
	return LoadFile(location.ConfigPath)
}

// LoadFile reads the explicitly supplied configuration path without fallback.
func LoadFile(filename string) (*Config, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, fmt.Errorf("resolve configuration path %q: %w", filename, err)
	}
	f, err := os.Open(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s was not found at %q; run \"yas config init\" to create an example configuration: %w", Filename, absolute, err)
	}
	if err != nil {
		return nil, fmt.Errorf("open configuration %q: %w", absolute, err)
	}
	defer f.Close()
	c, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("configuration %q: %w", absolute, err)
	}
	return c, nil
}

// Parse applies built-in defaults and strictly decodes one YAML document.
// Validation is separate so commands can apply explicit overrides first.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read YAML: %w", err)
	}
	if err := validateWorkingDirectoryType(data); err != nil {
		return nil, err
	}
	c := &Config{
		GitLab:  GitLabConfig{URL: "https://gitlab.com", TokenEnv: "GITLAB_TOKEN"},
		Tagging: TaggingConfig{Concurrency: 8, Timeout: "2m"},
		Release: ReleaseConfig{Concurrency: 8, Timeout: "2m"},
		Building: BuildingConfig{
			Concurrency: 2,
			Timeout:     "30m",
			Defaults: BuildOptions{
				Context: ".", Dockerfile: "Dockerfile",
				NoCache: boolPointer(false), Pull: boolPointer(false),
				BuildArgs: map[string]string{},
			},
		},
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("configuration is empty")
		}
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	if c == nil {
		return nil, errors.New("configuration is empty")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("decode trailing YAML: %w", err)
		}
		return nil, errors.New("only one YAML document is allowed")
	}
	return c, nil
}

func (c *Config) Validate() error {
	if err := c.ValidateCommon(); err != nil {
		return err
	}
	if err := c.ValidateTagging(); err != nil {
		return err
	}
	if err := c.ValidateBuilding(); err != nil {
		return err
	}
	return c.ValidateRelease()
}

func (c *Config) ValidateCommon() error {
	if c.Version != 1 {
		return errors.New("version must be 1")
	}
	if err := validateWorkingDirectory(c.WorkingDirectory); err != nil {
		return err
	}
	for _, group := range c.GitLab.Groups {
		if err := ValidateProject(group + "/project"); err != nil {
			return errors.New("gitlab.groups entries must be group paths such as my-group or my-group/subgroup")
		}
	}
	u, err := url.Parse(c.GitLab.URL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("gitlab.url must be an HTTP(S) URL without credentials, a query, or a fragment")
	}
	if strings.ContainsAny(c.GitLab.Token, "\r\n") {
		return errors.New("gitlab.token must not contain newline characters")
	}
	if (c.GitLab.TokenEnv != "" || strings.TrimSpace(c.GitLab.Token) == "") && !envName.MatchString(c.GitLab.TokenEnv) {
		return errors.New("gitlab.token_env must be a valid environment variable name")
	}
	return nil
}

func (c *Config) ValidateTagging() error {
	if err := validateExecution(c.Tagging.Concurrency, c.Tagging.Timeout); err != nil {
		return fmt.Errorf("tagging: %w", err)
	}
	if c.Tagging.Defaults.Tag != "" {
		if err := ValidateRef(c.Tagging.Defaults.Tag); err != nil {
			return fmt.Errorf("tagging.defaults.tag: %w", err)
		}
	}
	seen := make(map[string]bool)
	for i, entry := range c.Tagging.Repositories {
		entry = ApplyTagDefaults(entry, c.Tagging.Defaults)
		// The YAML defines repositories and optional pinned commits. The common
		// source branch is provided when running the batch, not in the config.
		if entry.Branch == "" {
			entry.Branch = "runtime-branch"
		}
		// Destination tags may also be supplied at runtime.
		if entry.Tag == "" {
			entry.Tag = "runtime-tag"
		}
		if err := ValidateTag(entry); err != nil {
			return fmt.Errorf("tagging.repositories[%d]: %w", i, err)
		}
		key := entry.Project + "\x00" + entry.Tag
		if seen[key] {
			return fmt.Errorf("tagging.repositories[%d]: duplicate destination %s tag %s", i, entry.Project, entry.Tag)
		}
		seen[key] = true
	}
	return nil
}

func (c *Config) ValidateBuilding() error {
	for _, prefix := range c.Building.ImagePrefixes {
		if err := ValidateImagePrefix(prefix); err != nil {
			return fmt.Errorf("building.image_prefixes: %w", err)
		}
	}
	if err := validateExecution(c.Building.Concurrency, c.Building.Timeout); err != nil {
		return fmt.Errorf("building: %w", err)
	}
	if err := ValidateBuildOptions(c.Building.Defaults); err != nil {
		return fmt.Errorf("building.defaults: %w", err)
	}
	seen := make(map[string]bool)
	for i, entry := range c.Building.Repositories {
		entry.BuildOptions = MergeBuildOptions(c.Building.Defaults, entry.BuildOptions)
		// The shared source Git tag is supplied when running a batch.
		if entry.Source.Tag == "" && entry.Source.Commit == "" {
			entry.Source.Tag = "runtime-tag"
		}
		if err := ValidateBuild(entry); err != nil {
			return fmt.Errorf("building.repositories[%d]: %w", i, err)
		}
		if entry.Image != "" && seen[entry.Image] {
			return fmt.Errorf("building.repositories[%d]: duplicate output image %s", i, entry.Image)
		}
		seen[entry.Image] = true
	}
	return nil
}

func (c *Config) ValidateRelease() error {
	if err := validateExecution(c.Release.Concurrency, c.Release.Timeout); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	seen := make(map[string]bool)
	for i, entry := range c.Release.Repositories {
		if err := ValidateProject(entry.Project); err != nil {
			return fmt.Errorf("release.repositories[%d]: %w", i, err)
		}
		if seen[entry.Project] {
			return fmt.Errorf("release.repositories[%d]: duplicate project %s", i, entry.Project)
		}
		seen[entry.Project] = true
	}
	return nil
}

func validateExecution(concurrency int, timeout string) error {
	if concurrency < 1 {
		return errors.New("concurrency must be greater than zero")
	}
	d, err := time.ParseDuration(timeout)
	if err != nil || d <= 0 {
		return errors.New("timeout must be a positive duration, for example 2m or 30m")
	}
	return nil
}

func ApplyTagDefaults(entry TagRepository, defaults TagDefaults) TagRepository {
	if entry.Tag == "" {
		entry.Tag = defaults.Tag
	}
	if entry.Message == "" {
		entry.Message = defaults.Message
	}
	return entry
}

// MergeBuildOptions returns an independent copy. Arguments merge by key and
// explicit Boolean false values override inherited true values.
func MergeBuildOptions(base, override BuildOptions) BuildOptions {
	result := BuildOptions{
		Context: base.Context, Dockerfile: base.Dockerfile,
		NoCache: copyBool(base.NoCache), Pull: copyBool(base.Pull),
		BuildArgs: make(map[string]string, len(base.BuildArgs)+len(override.BuildArgs)),
	}
	if override.Context != "" {
		result.Context = override.Context
	}
	if override.Dockerfile != "" {
		result.Dockerfile = override.Dockerfile
	}
	if override.NoCache != nil {
		result.NoCache = copyBool(override.NoCache)
	}
	if override.Pull != nil {
		result.Pull = copyBool(override.Pull)
	}
	for key, value := range base.BuildArgs {
		result.BuildArgs[key] = value
	}
	for key, value := range override.BuildArgs {
		result.BuildArgs[key] = value
	}
	return result
}

func ValidateTag(entry TagRepository) error {
	if err := ValidateProject(entry.Project); err != nil {
		return err
	}
	if entry.Branch == "" && entry.Commit == "" {
		return errors.New("branch or commit is required")
	}
	if entry.Branch != "" {
		if err := ValidateRef(entry.Branch); err != nil {
			return fmt.Errorf("branch: %w", err)
		}
	}
	if entry.Commit != "" {
		if err := ValidateCommit(entry.Commit); err != nil {
			return err
		}
	}
	if err := ValidateRef(entry.Tag); err != nil {
		return fmt.Errorf("tag: %w", err)
	}
	return nil
}

func ValidateBuild(entry BuildRepository) error {
	if err := ValidateProject(entry.Project); err != nil {
		return err
	}
	if (entry.Source.Commit == "") == (entry.Source.Tag == "") {
		return errors.New("source must contain exactly one of commit or tag")
	}
	if entry.Source.Commit != "" {
		if err := ValidateCommit(entry.Source.Commit); err != nil {
			return err
		}
	}
	if entry.Source.Tag != "" {
		if err := ValidateRef(entry.Source.Tag); err != nil {
			return fmt.Errorf("source.tag: %w", err)
		}
	}
	if entry.Image != "" {
		if err := ValidateImage(entry.Image); err != nil {
			return err
		}
	}
	return ValidateBuildOptions(entry.BuildOptions)
}

var (
	projectSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	projectID      = regexp.MustCompile(`^[1-9][0-9]*$`)
	commitSHA      = regexp.MustCompile(`^(?:[0-9a-fA-F]{7,40}|[0-9a-fA-F]{64})$`)
	envName        = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	imageTag       = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	imagePart      = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	registryHost   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?(?::[0-9]+)?$`)
	windowsDrive   = regexp.MustCompile(`^[A-Za-z]:`)
)

func ValidateProject(project string) error {
	if projectID.MatchString(project) {
		return nil
	}
	parts := strings.Split(project, "/")
	if len(parts) < 2 {
		return errors.New("project must be a positive numeric ID or full namespace/project path")
	}
	for _, part := range parts {
		if !projectSegment.MatchString(part) || part == "." || part == ".." || strings.HasSuffix(part, ".") {
			return errors.New("project must be a positive numeric ID or full namespace/project path without URL syntax or traversal")
		}
	}
	return nil
}

func ValidateCommit(commit string) error {
	if !commitSHA.MatchString(commit) {
		return errors.New("commit must be a hexadecimal SHA prefix of 7–40 characters or a full 64-character SHA")
	}
	return nil
}

// ValidateRef checks the standard Git reference-name restrictions without
// requiring the git executable or cloning a repository.
func ValidateRef(ref string) error {
	invalid := ref == "" || ref == "@" || strings.HasPrefix(ref, "/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.Contains(ref, "..") || strings.Contains(ref, "@{")
	for _, ch := range ref {
		if ch <= ' ' || ch == 127 || strings.ContainsRune(`~^:?*[\`, ch) {
			invalid = true
		}
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			invalid = true
		}
	}
	if invalid {
		return errors.New("must be a valid, nonempty Git reference name")
	}
	return nil
}

// ValidateGitRef is the explicit name used by interactive input validation.
func ValidateGitRef(ref string) error { return ValidateRef(ref) }

// ValidateImagePrefix accepts a repository namespace, optionally including a
// registry host/port. Prefixes contain neither a URL scheme nor an image tag.
func ValidateImagePrefix(prefix string) error {
	if prefix == "" || strings.HasPrefix(prefix, "/") {
		return errors.New("image prefix must be a nonempty registry/namespace path")
	}
	if err := ValidateImage(strings.TrimSuffix(prefix, "/") + "/yas-prefix-check:latest"); err != nil {
		return fmt.Errorf("invalid image prefix (use a registry/namespace without a URL scheme or tag): %w", err)
	}
	return nil
}

func ValidateImage(image string) error {
	colon := strings.LastIndex(image, ":")
	if colon <= strings.LastIndex(image, "/") || colon <= 0 || !imageTag.MatchString(image[colon+1:]) {
		return errors.New("image must include an explicit Docker tag, for example service:1.2.0")
	}
	name := image[:colon]
	if len(name) > 255 {
		return errors.New("image repository name exceeds 255 characters")
	}
	parts := strings.Split(name, "/")
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		if !registryHost.MatchString(parts[0]) {
			return errors.New("image registry hostname or port is invalid")
		}
		parts = parts[1:]
	}
	for _, part := range parts {
		if !imagePart.MatchString(part) {
			return errors.New("image repository must use lowercase Docker repository names")
		}
	}
	return nil
}

func ValidateBuildOptions(options BuildOptions) error {
	if err := validateRelativePath(options.Context, true); err != nil {
		return fmt.Errorf("context: %w", err)
	}
	if err := validateRelativePath(options.Dockerfile, false); err != nil {
		return fmt.Errorf("dockerfile: %w", err)
	}
	for key, value := range options.BuildArgs {
		if !envName.MatchString(key) {
			return fmt.Errorf("build_args key %q is not a valid argument name", key)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("build_args value for %q must not contain NUL characters", key)
		}
	}
	return nil
}

// ValidateRelativePath validates a repository-relative directory path.
func ValidateRelativePath(value string) error { return validateRelativePath(value, true) }

func validateRelativePath(value string, allowDot bool) error {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") || path.IsAbs(value) || windowsDrive.MatchString(value) {
		return errors.New("must be a nonempty repository-relative path using forward slashes")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return errors.New("must not contain parent-directory traversal")
		}
	}
	if !allowDot && path.Clean(value) == "." {
		return errors.New("must identify a Dockerfile, not a directory")
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	return boolPointer(*value)
}
