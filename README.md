# yas — yongsan at your service

A Go command-line application for concurrent GitLab tagging and Docker builds. Tag one repository or a configured list, and build images from an exact commit or Git tag without cloning repository history. Missing required inputs are prompted for in an interactive terminal.

## Requirements and installation

- Go 1.23 or newer to build the application.
- A GitLab personal, group, or project access token with access to the selected projects.
- Docker CLI on `PATH` and a reachable Docker daemon for actual builds. Docker is not needed for tagging, configuration commands, or dry runs.

From this repository:

```bash
go build -o bin/yas ./cmd/yas
./bin/yas version
```

The examples below use `yas`. Add the built binary's directory to `PATH`, install it in a directory already on `PATH`, or replace `yas` with the absolute path to your binary. Configuration is always resolved from your **current working directory**, regardless of where the executable lives.

To embed a release version:

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/yas ./cmd/yas
```

Git does not need to be installed to run `yas`: repository operations use GitLab's REST API.

## Quick start

Run the following in the directory where you want to keep your operation configuration:

```bash
yas config init
# Edit yas.yaml with your GitLab URL, repository lists, source refs, and image names.
yas config validate

# Set gitlab.token in yas.yaml to your access token.

yas tag batch --branch release/1.2 --dry-run
yas build batch --tag v1.2.0 --dry-run
```

After reviewing the dry-run results, run the operation without `--dry-run`:

```bash
yas tag batch --branch release/1.2
yas build batch --tag v1.2.0
```

`config init` creates a commented example and refuses to overwrite an existing file. Replace the example project identifiers before performing operations. You can also use [yas.example.yaml](yas.example.yaml) as a reference.

### The fixed configuration file

All tagging and build commands require `yas.yaml` in the current working directory. There is no `--config` option, no search in parent directories, and no prompt for an alternative filename. A missing file produces an error directing you to `yas config init`.

`yas config init`, `yas help`, command-specific `--help`, and `yas version` work without a configuration file. `yas config validate` needs the file but contacts neither GitLab nor Docker and does not require a token.

## GitLab authentication

Put your access token directly in `yas.yaml`:

```yaml
gitlab:
  url: https://gitlab.com
  token: "your-gitlab-access-token"
```

A nonempty `gitlab.token` takes precedence over environment authentication. Keep your local `yas.yaml` private; it is already excluded by this repository's `.gitignore`.

Alternatively, leave `token` empty and use `gitlab.token_env` to name an environment variable containing the token (default: `GITLAB_TOKEN`). The CLI does not prompt for a token or load `.env` files automatically.

For classic access-token scopes:

- Use `api` for tagging because the command creates tags through the REST API.
- A build-only token can use `read_api` for the read-only branch/tag/commit and source-archive requests used by this application. A `read_repository` scope alone should not be assumed to cover all these REST endpoints.

The token's scope and its owner's project access must both allow the requested operation. Group tokens are limited to their group; project tokens are limited to their project. Protected-tag rules still apply when creating tags. See [GitLab access-token scopes](https://docs.gitlab.com/security/tokens/access_token_scopes/) and [protected tags](https://docs.gitlab.com/user/project/protected_tags/).

For a self-managed server or an alternative token variable:

```yaml
gitlab:
  url: https://gitlab.example.com
  token_env: COMPANY_GITLAB_TOKEN
```

```bash
export COMPANY_GITLAB_TOKEN='your-access-token'
yas tag batch --branch release/1.2
```

`--gitlab-url` overrides the server URL. An explicit `--token-env NAME` selects environment authentication for that invocation, overriding a configured token. The URL can include a GitLab deployment prefix. Keep credentials out of the URL.

## Commands and interactive input

| Command | Purpose |
| --- | --- |
| `yas` | Show an operation menu when standard input is an interactive terminal; otherwise show help |
| `yas tag one` | Create a Git tag in one repository |
| `yas tag delete one` | Delete a named Git tag from one project |
| `yas tag delete batch` | Delete the same named Git tag from tagging projects |
| `yas tag batch` | Tag selected entries from `tagging.repositories` |
| `yas build one` | Build one image from a commit or Git tag |
| `yas build batch` | Build selected entries from `building.repositories` |
| `yas config init` | Create the example `yas.yaml` without overwriting a file |
| `yas config validate` | Validate the entire configuration locally |
| `yas help [command]` | Display help, such as `yas help tag one` |
| `yas version` | Print the application version |

Every command also supports `--help`/`-h`.

### Optional group selection

Add group paths to `gitlab.groups` to avoid retyping them in single-repository prompts:

```yaml
gitlab:
  url: https://gitlab.com
  token: "your-gitlab-access-token"
  groups:
    - my-group
    - company/platform
```

When `--project` is missing, `yas tag one`, `yas tag delete one`, and `yas build one` offer those groups plus **Skip (enter full project path or ID)**. Select `company/platform` and enter `api` to use `company/platform/api`; enter `subgroup/api` to use `company/platform/subgroup/api`. After selecting a group, type only the path within it. Choose Skip to enter a full namespace/project path or numeric project ID without an automatic prefix.

Omit `groups` or set `groups: []` to bypass group selection and enter the full project path directly. Explicit `--project` values bypass the menu and are never prefixed. Non-interactive commands still require `--project`. Batch group menus are derived from the project paths in the applicable repository list, independently of `gitlab.groups`; groups never discover remote repositories.

### Group options for commands and batches

Single-repository command flags use the full project path or numeric ID directly:

```bash
yas tag one --project yas-dummy-group/dummy-1 --branch main --tag v1.2.0
yas tag delete one --project yas-dummy-group/dummy-1 --tag v1.2.0
yas build one --project yas-dummy-group/dummy-1 --tag v1.2.0 --image dummy-1:1.2.0
```

The `--group` and `--all-groups` flags apply only to batches. Interactive single-repository flows still offer the optional `gitlab.groups` menu when `--project` is missing.

Interactive `tag batch`, `tag delete batch`, and `build batch` offer groups extracted from their own configured project lists plus **All groups**. For example, `yas-dummy-group/dummy-1` contributes the group `yas-dummy-group`; `company/team/service` contributes `company/team`. A numeric-only entry is offered under **Project IDs (no group path)**. Selecting a group limits execution to that exact full namespace, excluding other groups and nested namespaces.

The same filters are available without prompts:

```bash
yas tag batch --branch release/1.2 --group yas-dummy-group --tag v1.2.0
yas tag delete batch --group yas-dummy-group --tag v1.2.0
yas build batch --tag v1.2.0 --group yas-dummy-group
# Explicitly operate across the entire configured list:
yas tag batch --branch release/1.2 --all-groups
```

`--group` and `--all-groups` are mutually exclusive. An unknown group fails before any work. Repeatable `--only` values remain full configured project identifiers; when combined with `--group`, every selected project must belong to that group, otherwise the command fails. Supplying `--only` bypasses the group menu. In non-interactive mode, omitting group filters retains the existing behavior of processing the full list. Group filtering only selects existing list entries; it does not add group prefixes to batch entries or discover projects on GitLab.

### Prompting

With an existing configuration and interactive standard input:

```bash
yas tag one
```

The CLI asks for a project, a source choice, the source branch/commit, and the destination Git tag. The source menu accepts a numbered choice: branch, commit, or commit with branch validation. Invalid interactive answers are explained and requested again.

Supplying some inputs skips those prompts:

```bash
yas tag one --project my-group/service-a --branch main
# Prompts only for the destination Git tag.
```

`yas build one` prompts for the project, source type (Git tag or commit), source value, and an optional output image override (Enter accepts the generated name). Optional settings use configuration or built-in defaults; the CLI does not ask you to fill every optional field.

Batch commands obtain repository details from their own configuration list and do not prompt for missing repository fields. A missing or invalid `yas.yaml` is reported before operation prompts begin.

For CI and scripts, provide all required flags and use `--non-interactive`. Missing values produce an error instead of waiting for input. This behavior also applies automatically when standard input is not a terminal; piping answers into a command does not enable prompts.

## Tagging

### Tag one repository

Tag the current tip of a branch:

```bash
yas tag one \
  --project my-group/service-a \
  --branch main \
  --tag v1.2.0
```

Tag a specific commit:

```bash
yas tag one \
  --project my-group/service-a \
  --commit 0123456789abcdef0123456789abcdef01234567 \
  --tag v1.2.0
```

The hash above is illustrative. Short hashes such as `f63850b` are also accepted (at least 7 hexadecimal characters). GitLab resolves the prefix to a full SHA before any tag creation or build. Missing or ambiguous prefixes fail; use a longer hash in that case. If GitLab returns 404 for a short hash, yas falls back to paginated commit metadata across all branches and tags, checks for a unique commit match, and verifies its full SHA. This fallback downloads no source files, but can be slower on large repositories. It is limited to 100,000 listed commits and the job timeout; incomplete searches fail rather than assuming uniqueness. Only commits exposed by GitLab’s listing can be found.

```bash
yas tag one --project my-group/service-a --commit f63850b --tag v1.2.0
yas build one --project my-group/service-a --commit f63850b --image service-a:1.2.0
```

Supply both a commit and branch to verify reachability before tagging:

```bash
yas tag one \
  --project my-group/service-a \
  --commit 0123456789abcdef0123456789abcdef01234567 \
  --branch release/1.2 \
  --tag v1.2.0 \
  --message "Release 1.2.0"
```

The selected commit must be the resolved branch tip or an ancestor of it. It does not have to be the latest commit. Branch-only tagging resolves the branch once and creates the tag at that SHA.

Project identifiers can be positive numeric IDs or full namespace paths, including nested groups:

```bash
yas tag one --project 12345 --branch main --tag v1.2.0
yas tag one --project company/platform/api --branch main --tag v1.2.0
```

`--message` adds an annotation to a new tag. Without an effective message, the new tag is lightweight. Existing tags are handled as follows:

- Same destination commit: report `skipped` and leave the existing tag and its annotation unchanged.
- Different destination commit: report `failed`; the application never moves the tag.

### Tag a configured list

Choose a source branch and destination tag for each invocation. Tag names can be omitted from the tagging config; supply `--tag` or answer the interactive tag prompt. If any selected entry lacks an effective tag and `--tag` is omitted, the prompt asks once and applies that tag to all selected entries. Non-interactive commands require `--tag` in that case.

Choose a source branch once for the invocation. `--branch` applies to every selected repository and has no implicit `main` default. If omitted in an interactive terminal, yas asks for the branch after group selection. Non-interactive commands fail if `--branch` is missing. Remove old `branch:` fields from tagging repository entries; these fields are no longer accepted in YAML. The config now holds the project list, destination tags, optional annotations, and optional pinned commits. A missing branch on a repository fails that job while other selected repositories continue.


```bash
yas tag batch --branch release/1.2
yas tag batch --branch release/1.2 --tag v2.0.0 --message "Release 2.0.0"
yas tag batch --branch release/1.2 --concurrency 4
yas tag batch --branch release/1.2 --only my-group/service-a --only my-group/service-b
```

Only `tagging.repositories` is used. Supply the common source branch with `--branch` or enter it once in the interactive flow. Each selected repository uses that branch. Optional configured commits are tagged only after verifying they belong to that branch. `--tag` and `--message` override the destination tag and annotation for every selected entry. Batch tagging requires a shared `--branch` (prompted if missing); it does not accept a shared `--commit`.

`--only` is repeatable and matches configured project identifiers exactly. For example, use `--only 12345` when the entry uses the numeric ID `12345`, rather than its namespace path. An unknown identifier is an error. All entries matching a selected identifier are included, so a tagging list can contain multiple distinct destination tags for one project.

Listing a repository inside a group does not discover or include other projects in that group.

### Delete a tag

Delete one exact Git tag from a single repository:

```bash
yas tag delete one --project my-group/service-a --tag v1.2.0
```

Delete the same tag from every distinct project in `tagging.repositories` concurrently:

```bash
yas tag delete batch --tag v1.2.0
# Preview without deleting:
yas tag delete batch --tag v1.2.0 --dry-run
# Optionally restrict projects or concurrency:
yas tag delete batch --tag v1.2.0 --only my-group/service-a --concurrency 4
```

Both commands prompt for missing required inputs in an interactive terminal. The tag to delete must be supplied through `--tag` or a prompt; it never comes from `defaults.tag` or an entry's `tag`. Batch deletion uses only project identifiers from the tagging list, ignores branch/commit/tag/message creation settings, and runs each distinct configured project identifier once. It does not use the building list.

Results are `deleted`, `skipped` for an already-absent tag in an accessible project, `planned` for an existing tag in a dry run, or `failed`. Permission errors and missing/inaccessible projects are failures. Independent jobs continue and any failure returns exit code 1. Existing timeout, JSON, non-interactive, authentication, and batch selection flags apply.

Deletion removes the remote Git tag regardless of its commit, and does not change local tags or delete Docker image tags. GitLab deletion permissions and protected-tag rules apply. These commands issue actual deletions unless `--dry-run` is supplied.

## Docker builds

### Build one image

From a Git tag:

```bash
yas build one \
  --project my-group/service-a \
  --tag v1.2.0 \
  --image service-a:1.2.0
```

From a commit:

```bash
yas build one \
  --project my-group/service-a \
  --commit 0123456789abcdef0123456789abcdef01234567 \
  --image service-a:1.2.0
```

Builds require exactly one source: `--tag` or `--commit`. Here **`--tag` is the source Git tag**, and **`--image` is the output Docker image name**. Explicit output image overrides must include a tag, such as `service-a:1.2.0` or `registry.example.com/team/service-a:1.2.0`.

For a service in a subdirectory:

```bash
yas build one \
  --project my-group/platform \
  --tag v1.2.0 \
  --context services/api \
  --dockerfile Dockerfile.release \
  --image platform-api:1.2.0 \
  --build-arg APP_VERSION=1.2.0
```

`context` is relative to the repository root. `dockerfile` is relative to the selected context. In this example, the source Dockerfile is `services/api/Dockerfile.release`. Paths must stay inside the selected repository/context and use forward slashes.

Every file needed by the Dockerfile must be available in the context. If your Dockerfile needs files from multiple repository directories, select a sufficiently broad context, such as `.`, and set the Dockerfile path within it.

### Build image and context overrides

The build configuration examples and local config omit `image` and `context`. Defaults are repository-name:runtime-tag (or resolved commit SHA for a single commit build), and context `.` (repository root). Choose overrides per invocation:

```bash
# Single repository:
yas build one --project group/service --tag v1 --image custom-service:test --context services/api --non-interactive
# Shared batch context and an image override for one selected project:
yas build batch --group my-group --tag v1 --context services/api --image my-group/service=custom-service:test --non-interactive
```

On `build one`, `--image` takes one image name with a Docker tag. On `build batch`, repeat `--image PROJECT=NAME:TAG` for each selected project to override; project identifiers must exactly match the config entries, and unselected/unknown projects or duplicate override keys are errors. Other selected projects use generated defaults. The selected image prefix also applies to these overrides.

Interactive single builds ask for an optional image override and context; Enter accepts the defaults. Interactive batches ask once for the shared context and automatically name every image from its repository and source tag/commit; they never prompt for individual image names. Only explicit batch `--image PROJECT=NAME:TAG` flags override those generated names; legacy configured image values are ignored. Explicit command-line overrides bypass their corresponding prompts. `--context` applies to all selected batch entries; if their layouts differ, run separate batches using `--only`. Non-interactive builds use defaults automatically when these options are omitted. Dockerfile paths remain relative to the selected context.

### Default Docker image names

`--image` and per-entry `image` are optional. When omitted, yas uses the repository's final path component (lowercased) as the image repository name, not the group path or a display name containing spaces. Numeric project IDs are resolved through GitLab to get that repository slug.

- `group/dummy-1` from Git tag `v1.2.0` becomes `dummy-1:v1.2.0`.
- A commit build uses its resolved full SHA as the Docker tag: `dummy-1:<full-sha>`.
- A selected image prefix is prepended, for example `registry.example.com/team/dummy-1:v1.2.0`.
- Explicit command-line or interactive image overrides replace the generated name.

```bash
yas build one --project group/dummy-1 --tag v1.2.0 --non-interactive
yas build one --project group/dummy-1 --commit f63850b7 --image-prefix registry.example.com/team --non-interactive
```

The interactive image prompt accepts Enter to use the default, or a custom image name with an explicit Docker tag. In batch configs, simply omit `image` to use the same naming rules. Use command-line flags or interactive prompts for overrides. If a Git tag cannot be used as a Docker tag (for example `release/v1` contains `/`), provide an explicit valid image override such as `dummy-1:release-v1`.

Every final output image name is reserved once per batch. If two repositories generate the same name/tag, one job claims that target and another fails rather than overwriting it; assign distinct image overrides for repositories with identical names. Images and resolved defaults appear in progress and final results.

### Choose an output image prefix

An image prefix is the registry hostname plus optional repository namespace, such as `registry.gitlab.stelabs.com/registry/container-images`. Configure choices under `building.image_prefixes`:

```yaml
building:
  image_prefixes:
    - registry.gitlab.stelabs.com/registry/container-images
    - localhost:5000/testing
```

The single-image interactive flow offers these options before asking for the image name. Selecting the first prefix and entering `my-service:v1.2.0` produces `registry.gitlab.stelabs.com/registry/container-images/my-service:v1.2.0`. A **Skip** choice keeps the name exactly as supplied. An empty/omitted list skips the menu. An explicit `--image` on `build one` bypasses the optional menu; use `--image-prefix` if you want it prefixed.

Batch builds offer the prefix menu once after group selection and apply the choice to every selected entry's configured image name. Prefixes never alter GitLab project paths or source refs. Fully specified non-interactive commands retain their image names unless an explicit prefix flag is provided.

```bash
yas build one --project group/my-service --tag v1.2.0 --image my-service:v1.2.0 --image-prefix registry.gitlab.stelabs.com/registry/container-images --non-interactive
yas build batch --tag v1.2.0 --group my-group --image-prefix registry.gitlab.stelabs.com/registry/container-images --non-interactive
yas build batch --tag v1.2.0 --all-groups --no-image-prefix --non-interactive
```

`--image-prefix` and `--no-image-prefix` are mutually exclusive and work on both build commands. Explicit prefix flags can name a valid prefix outside the list. Prefixes contain no URL scheme or image tag; a trailing slash is accepted. An image already starting with the selected prefix is left unchanged. A fully qualified image using a different registry is rejected rather than having a second registry prepended; choose Skip/`--no-image-prefix` or change that entry to a relative image name. A relative name can include additional namespaces, for example `team/service:v1`. Duplicate final output names are checked after prefixing. Progress and final results display the final image names.

This sets local image names only. It does not push images or export `.tar` files. If you later save and load the image, the saved name includes the selected prefix.

### Build a configured list

Choose one source Git tag per batch using `--tag`, or enter it once when prompted after group selection. The shared tag is resolved separately in every selected repository; missing tags fail only the affected job. There is no default source tag. Non-interactive batches require `--tag`.

```bash
yas build batch --group my-group --tag v1.2.0 --concurrency 4 --non-interactive
yas build batch --tag v1.2.0  # choose group, enter source tag, then choose image prefix
```

Build configs need only project paths and optional image/build settings. Remove per-entry `source` blocks; the examples and local config no longer include them. Older `source` entries are still parsed for compatibility, but the explicitly selected runtime batch tag takes precedence over them, including pinned commits. To build a specific commit, use `build one --commit`. Generated image names use the selected runtime tag; explicit runtime image overrides still control output naming.


```bash
yas build batch --tag v1.2.0
yas build batch --tag v1.2.0 --concurrency 3
yas build batch --tag v1.2.0 --only my-group/service-a
yas build batch --tag v1.2.0 --no-cache --pull
```

Only `building.repositories` is used. The shared source comes from runtime `--tag` or the tag prompt. Output names default to repository-name:runtime-tag, with optional command-line or interactive image overrides. Batch builds do not accept a shared `--commit`; per-project output overrides use repeatable `--image PROJECT=NAME:TAG`.

Both build commands accept:

| Flag | Meaning |
| --- | --- |
| `--context PATH` | Repository directory used as the build context |
| `--dockerfile PATH` | Dockerfile relative to that context |
| `--build-arg KEY=VALUE` | Build argument; repeatable, with later values winning for the same key |
| `--no-cache[=true\|false]` | Disable or enable Docker build-cache reuse |
| `--pull[=true\|false]` | Control refreshing referenced base images |

On batch builds, explicit build flags apply to every selected entry. Build arguments are ordinary build configuration, not a mechanism for supplying secrets.

### Source downloads and build environment

The application resolves the supplied commit or Git tag to a full commit SHA, requests that SHA's GitLab source archive, and builds from a temporary directory. A subdirectory context requests only that repository subdirectory. No repository history is cloned, and no `.git` directory is supplied. Temporary source files are removed after success, failure, or cancellation.

Docker reads `.dockerignore` from the selected build context according to its normal rules. These rules affect what Docker receives; they do not filter the files downloaded in the GitLab archive.

Builds use the `docker build` command and inherit your Docker configuration, environment, current Docker context, registry credentials, and daemon settings. The image is loaded into that daemon's image store. The application does not push images to a registry, even when the image name includes a registry hostname.

Source/archive limitations:

- Builds that need Git history or run commands such as `git describe` cannot obtain that information from this source snapshot. Supply required version information through build arguments or adapt the Dockerfile.
- Git submodules are unsupported and are not fetched. A `.gitmodules` file in the selected context causes the build to fail with an explanation.
- Archive requests explicitly include Git LFS blobs. Their availability still depends on the GitLab repository and server.
- Archives are limited to 1 GiB compressed, 2 GiB expanded tar data including metadata/padding, 1 GiB per file, and 100,000 archive entries. Unsupported hard links/special files and unsafe archive paths are rejected. Source symlinks must resolve to existing files or directories inside the selected context; broken, cyclic, or escaping links are rejected.
- A Git tag is resolved once per job. If someone moves it between separate runs, the next run can select a different commit. A configured commit SHA pins the source across runs; Docker dependencies and base images can still change independently.

GitLab imposes archive and API rate limits. Concurrency is a maximum number of active jobs, not a guarantee of request throughput. Read requests retry transient connection failures and selected temporary HTTP errors, including `429`, while respecting `Retry-After` and the job deadline. See [GitLab source archives](https://docs.gitlab.com/api/repositories/#retrieve-file-archive-from-a-repository).

## YAML configuration

```yaml
version: 1

gitlab:
  url: https://gitlab.com
  token: "your-gitlab-access-token"

tagging:
  concurrency: 8
  timeout: 2m
  repositories:
    - project: my-group/service-a
    - project: my-group/subgroup/service-b
      message: Hotfix release

building:
  concurrency: 2
  timeout: 30m
  defaults:
    dockerfile: Dockerfile
    no_cache: false
    pull: false
  repositories:
    - project: my-group/service-a
    - project: my-group/subgroup/service-c
```

Replace the illustrative commit above with a real full SHA. The lists are independent: this example tags services A and B but builds services A and C. Either list may be empty for a configuration used only by the other operation; running a batch against an empty list fails clearly.

### Fields and defaults

| Field | Default / behavior |
| --- | --- |
| `version` | Required; must be `1` |
| `gitlab.url` | `https://gitlab.com` |
| `gitlab.token` | Optional access token stored directly in the config; takes precedence over environment authentication |
| `gitlab.groups` | Optional list of group/subgroup paths for interactive single-repository selection; defaults to an empty list |
| `gitlab.token_env` | `GITLAB_TOKEN`; used when `token` is empty or `--token-env` is supplied |
| `tagging.concurrency` | `8`; must be positive |
| `tagging.timeout` | `2m`; positive time limit for each repository job |
| `tagging.defaults.tag` | Optional destination tag fallback for batch entries |
| `tagging.defaults.message` | Optional annotation fallback for new tags |
| `tagging.repositories[].project` | Required project ID or full namespace/project path |
| `tagging.repositories[].branch` | Removed; supply `--branch` or answer the runtime branch prompt |
| `tagging.repositories[].commit` | Optional full or abbreviated commit SHA; validated against the batch branch selected at runtime |
| `tagging.repositories[].tag` | Entry's destination Git tag; otherwise use tagging default |
| `tagging.repositories[].message` | Entry's tag annotation; otherwise use tagging default |
| `building.image_prefixes` | Optional list of output registry/namespace prefixes; empty by default |
| `building.concurrency` | `2`; must be positive |
| `building.timeout` | `30m`; positive time limit for each repository job |
| `building.defaults.dockerfile` | `Dockerfile` within the context |
| `building.defaults.no_cache` | `false` |
| `building.defaults.pull` | `false` |
| `building.defaults.build_args` | Empty mapping |
| `building.repositories[].project` | Required project ID or full namespace/project path |
| `building.repositories[].source` | Not needed; legacy values are superseded by the required runtime batch tag |
| Build options on a repository entry | Override corresponding build defaults |

YAML fields are checked strictly: unknown fields, duplicate YAML keys, and multiple documents are errors. Commit hashes must be hexadecimal (7–40 characters, or a full 64-character SHA), and references, image names, paths, and build-argument names are validated. Duplicate tagging destinations for the same configured project/tag and duplicate output image names are rejected before execution.

### Override precedence

For an applicable setting:

```text
Explicit command-line flag
  > repository entry
  > operation defaults in yas.yaml
  > built-in defaults
```


```bash
yas build batch --tag v1.2.0 --no-cache=false --pull=false
```

The `one` commands use the shared connection settings, operation timeouts, applicable build defaults, and default tag annotation. They do not inherit a project, source, destination Git tag, or output image from a batch entry. Project and source fields must be supplied through flags or interactive answers; an omitted output image uses the repository-based default. In particular, `tagging.defaults.tag` is a batch fallback, not the destination tag for `tag one`.

`config validate` checks the entire YAML configuration as written. An operation validates its applicable settings and selected effective entries before scheduling jobs. A missing/invalid setting in an unrelated repository list does not supply input to a `one` command.

## Shared flags

| Flag | Applies to / effect |
| --- | --- |
| `--gitlab-url URL` | Override the operation's GitLab server URL |
| `--token-env NAME` | Override the environment variable containing the token |
| `--timeout DURATION` | Override each repository job's time limit, such as `5m` or `1h` |
| `--dry-run` | Resolve sources and report planned work without tag creation or image builds |
| `--format text\|json` | Operation result format; default `text` |
| `--no-progress` | Hide live operation progress; Docker logs and final results remain |
| `--non-interactive` | Error on missing required inputs instead of prompting |
| `--concurrency N` | Batch commands only; maximum simultaneous repository jobs |
| `--only PROJECT` | Batch commands only; repeatable selection from that operation's configured list |

Run `yas tag one --help`, `yas tag batch --help`, `yas build one --help`, or `yas build batch --help` for command-specific flags. Configuration commands validate or write the fixed file; connection overrides and execution flags are intended for tagging/build operations.

## Live progress

Tagging, tag deletion, and Docker builds display live timestamped progress on stderr by default, for interactive flows and fully specified commands alike. Each event identifies the job number, operation, repository, and target tag/image. START and STEP events show source resolution, branch validation, tag checks, archive download/extraction, and Docker execution as applicable. DONE events report the actual result and elapsed time, including skipped/failed/planned operations. Every event includes active-job and completed-job counts. Docker output remains labeled by repository and image.

For example, this illustrative output shows two jobs overlapping (actual timing and order vary):

```text
Running 3 repository job(s), concurrency=2 (maximum simultaneous jobs).
[14:03:00.001] job 1/3 [tag group/a -> v1] START resolving source commit | active=1 completed=0/3
[14:03:00.002] job 2/3 [tag group/b -> v1] START resolving source commit | active=2 completed=0/3
[14:03:00.140] job 2/3 [tag group/b -> v1] DONE created (138ms) | active=1 completed=1/3
[14:03:00.141] job 3/3 [tag group/c -> v1] START resolving source commit | active=2 completed=1/3
```

Concurrency is the maximum number of repository jobs; active jobs may be waiting for GitLab, downloading sources, or executing Docker. A single-repository command has one job. Progress is event-based, not a byte-level percentage or a guarantee that every active job is consuming CPU simultaneously. Dry runs show their actual checks and finish as planned/skipped/failed, not as completed writes or builds.

```bash
yas tag batch --branch release/1.2 --group my-group --concurrency 4
yas build batch --tag v1.2.0 --concurrency 2
# Suppress live operation events (Docker logs and final results remain):
yas tag batch --branch release/1.2 --no-progress
# JSON stdout stays separate from progress and Docker logs on stderr:
yas build batch --tag v1.2.0 --format json > results.json
```

The final result table/JSON remains in configuration order; live progress follows actual execution order.

## Dry runs, results, and failures

```bash
yas tag batch --branch release/1.2 --dry-run
yas build batch --tag v1.2.0 --dry-run --format json
```

A dry run still requires the configured token and contacts GitLab. Tagging checks source existence, branch reachability where requested, and existing tag conflicts. Building resolves source refs but does not download archives, inspect Dockerfiles or build contexts remotely, contact Docker, or prove that a future build will succeed. A tagging dry run also cannot prove permission to create a new protected tag because it performs no write.

Operations report one result per repository job in input/configuration order, even when jobs finish in a different order. Status values are `created`, `deleted`, `built`, `skipped`, `planned`, and `failed`. Results include the resolved commit SHA when available; JSON also contains operation/source/target fields, job duration, and an error for failures.

```bash
yas build batch --tag v1.2.0 --non-interactive --format json > results.json
```

JSON results go to standard output as an array. Prompts, errors, and labeled Docker logs go to standard error, keeping standard output usable for scripts. Text output includes a table and status totals.

Jobs run through a bounded worker pool. A repository failure does not stop unrelated repositories, and a failed batch can have successful tags or images. There is no rollback. Re-running a tagging batch skips tags already pointing at the requested commit; successful Docker builds can replace an existing local image tag normally.

The per-job timeout begins when a worker starts that job. It includes GitLab resolution, retry delays, source download/extraction, and the Docker command as applicable; queue waiting, configuration loading, and interactive prompts are outside this timeout. `Ctrl+C`/SIGINT and SIGTERM cancel outstanding work and running Docker commands. Already completed tags and images remain, and temporary downloaded source is cleaned up.

| Exit code | Meaning |
| --- | --- |
| `0` | Every selected operation succeeded, was skipped as already satisfied, or was planned successfully |
| `1` | One or more operations failed, or an execution/output error occurred |
| `2` | Invalid/missing arguments, configuration, or credentials |

## Development and tests

```bash
go test -race ./...
go vet ./...
go build -o bin/yas ./cmd/yas
```

Tests exercise configuration parsing and precedence, GitLab API behavior with local HTTP servers, archive extraction and path/link checks, Docker argument construction using a test executable, and CLI execution/concurrency. Unit tests do not need a real GitLab token or a running Docker daemon. Testing against your GitLab server and Dockerfiles is still needed to verify project permissions and the actual build environment.
