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

The examples below use `yas`. Add the built binary's directory to `PATH`, install it in a directory already on `PATH`, or replace `yas` with the absolute path to your binary. You can run it from any directory: configuration lives in `bin/yas.yaml` beside the actual executable, after resolving symlinks. With the build command above, that file is `bin/yas.yaml`.

To embed a release version:

```bash
go build -ldflags "-X main.version=v1.0.0" -o bin/yas ./cmd/yas
```

Git does not need to be installed to run `yas`: repository operations use GitLab's REST API.

## Quick start

Build or install the executable first, then run the following from any directory:

```bash
yas config init
# Edit the full path printed by config init with your GitLab URL, access token,
# repository lists, source refs, and image settings.
yas config validate

yas tag batch --branch release/1.2 --dry-run
yas build batch --tag v1.2.0 --dry-run
```

After reviewing the dry-run results, run the operation without `--dry-run`:

```bash
yas tag batch --branch release/1.2
yas build batch --tag v1.2.0
```

`config init` creates a commented example beside the executable, prints its full path, and refuses to overwrite an existing file. Replace the example project identifiers before performing operations. You can also use [yas.example.yaml](yas.example.yaml) as a reference.

### The fixed configuration file

Tagging, builds, release scans, and configuration validation use `bin/yas.yaml` in the actual executable's directory. For example, `/tools/yas` uses `/tools/yas.yaml`; invoking a symlink to `/tools/yas` uses that same file. There is no `--config` option, working-directory or parent-directory fallback, or prompt for an alternative filename. A missing file produces an error directing you to `yas config init`.

Move or copy an existing configuration beside the executable manually; the application does not migrate it automatically. Use a built executable for initialization and normal runs: `go run` creates a temporary executable, so its configuration location is also temporary.

Relative output paths use the current working directory: default batch reports go under `./reports`, image archive batches under `./images`, and release drafts use `release-notes-<version>.yaml`. Explicit report, archive-parent, and release-draft paths retain their respective command meanings.

`yas config init`, `yas help`, command-specific `--help`, `yas version`, `yas release render`, and the interactive root operation menu work without a configuration file. Selecting an operation that uses configuration still loads its required file. `yas config validate` needs the file but contacts neither GitLab nor Docker and does not require a token.

## GitLab authentication

Put your access token directly in `bin/yas.yaml`:

```yaml
gitlab:
  url: https://gitlab.com
  token: "your-gitlab-access-token"
```

A nonempty `gitlab.token` takes precedence over environment authentication. Keep your local `bin/yas.yaml` private; it is already excluded by this repository's `.gitignore`.

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

To skip certificate verification for a self-managed GitLab server, add this option to your existing `gitlab` block:

```yaml
gitlab:
  url: https://gitlab.stelabs.com
  insecure_skip_verify: true
```

`gitlab.insecure_skip_verify` defaults to `false`. Enabling it leaves the server's identity unchecked and can expose your access token to an impersonating server. The option applies to the application's GitLab HTTPS requests.

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
| `yas release scan` | Collect new release commits across selected repositories and write an editable YAML draft |
| `yas release render` | Turn completed release-notes YAML into standalone HTML with selectable grouping |
| `yas config init` | Create the example `bin/yas.yaml` beside the executable without overwriting a file |
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

Batch commands obtain repository details from their own configuration list and do not prompt for missing repository fields. A missing or invalid `bin/yas.yaml` is reported before operation prompts begin.

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

This sets local image names only and does not push images. If you enable `--save`, the saved image retains the selected prefix when loaded again.

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
| `--save[=true\|false]` | Save each successfully built image as a Docker `.tar` archive; default `false` |
| `--save-dir PATH` | Parent directory for image archive batches; default `./images`; does not enable saving by itself |

On batch builds, explicit build flags apply to every selected entry. Build arguments are ordinary build configuration, not a mechanism for supplying secrets.

### Save built images

Use `--save` on either build command to run `docker save` immediately after each successful build:

```bash
yas build one --project my-group/service-a --tag v1.2.0 --save
yas build batch --tag v1.2.0 --save --save-dir ./release-images
# Load a saved archive into another Docker daemon:
docker load --input /path/to/release-images/20261125T093000.123456789Z/service-a_v1.2.0.tar
```

Interactive builds ask once, after the other build prompts, `Save built images as .tar files after build? [y/N]`. Enter or `no` leaves saving disabled. An explicit `--save` or `--save=false` bypasses this prompt. Non-interactive builds save only when `--save` is enabled; `--save-dir` selects the destination without enabling saves.

Each build invocation gets one unique archive batch ID based on UTC time, in the form `YYYYMMDDTHHMMSS.nnnnnnnnnZ`. All its saved images go into that subdirectory under `--save-dir`, including for a single-image build. Repeated runs use different subdirectories:

```text
images/
  20261125T093000.123456789Z/
    service-a_v1.2.0.tar
    service-b_v1.2.0.tar
  20261125T093005.987654321Z/
    service-a_v1.2.0.tar
    service-b_v1.2.0.tar
```

Archive filenames follow the application's portable `<image>_<tag>.tar` convention. The image component is the final repository basename, excluding any registry or namespace; for example `registry.example.com/team/service-a:v1.2.0` becomes `service-a_v1.2.0.tar`. The filenames have no random suffix; Docker itself does not mandate a `.tar` filename convention. Image tags, including registry and namespace prefixes, are retained inside the archive for `docker load`.

The batch directory is created only when the first successful build reaches the save step, so disabled saves, dry runs, and runs where every build fails create no archive directory. Missing parent directories are created as needed, and existing files are never overwritten. Two selected images that map to the same archive filename cause one job to fail before its Docker build; choose distinct image basenames or tags to resolve the collision. The built image stays in the current Docker daemon; saving does not push it to a registry. The default `./images/` directory is ignored by Git in this repository.

Each save runs within its repository job's existing timeout and concurrency slot. Failed builds do not attempt a save. If saving fails or is canceled, the incomplete archive is removed and the overall job fails, while the successful build is recorded separately and its local image remains available. Other batch jobs continue normally.

Text results include save status and archive paths. JSON adds `build_status`, `save_status`, `archive`, and `save_error` when applicable, plus `batch_id` and the absolute `archive_directory` once the batch directory exists. Batch HTML reports show the archive batch ID and directory, build and save outcomes separately, save totals, archive paths, and save errors. A successful requested build-and-save workflow retains overall status `built`; failure in either stage produces `failed`.

`--dry-run --save` marks the build and save as planned without invoking Docker, creating the archive directory, or writing archives. Source resolution still contacts GitLab as described under dry runs.

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

## Scan changes for release notes

`yas release scan` reads `release.repositories` in `bin/yas.yaml`, collects commits reachable from the target release that are absent from the baseline history, and writes an editable YAML release-notes draft for human completion. It makes no Git changes, creates no tags, and does not invoke Docker.

Configure the components to include in release notes:

```yaml
release:
  concurrency: 8
  timeout: 2m
  repositories:
    - project: my-group/service-a
    - project: my-group/service-b
    - project: my-group/subgroup/service-c
```

This list is required for release scanning and is independent of the other operation settings. An omitted or empty `release.repositories` produces a clear error; there is no repository-list selection prompt or fallback to another list.

```bash
# Collect commits added since the baseline release tag:
yas release scan --from 2.2.7 --to 2.2.8 --output drafts/release-2.2.8.yaml
# Detect each repository's previous stable version automatically:
yas release scan --to 2.2.8 --previous --group my-group
# Select an individual configured component:
yas release scan --only my-group/service-a --from 2.2.7 --to 2.2.8
# Draft an untagged release from the current branch tip:
yas release scan --from 2.2.7 --to-branch release/2.2 --version 2.2.8 --output drafts/release-2.2.8.yaml
```

Choose exactly one target: `--to TAG`, `--to-branch BRANCH`, or `--to-commit SHA`. An explicit `--from TAG` selects the baseline release tag. `--from` and `--previous` are mutually exclusive. `--version` sets the draft's release label without changing the scanned revisions; by default the label comes from the target tag, branch, or commit. Branch and commit targets support drafts before a release tag exists and require an explicit `--from`.

With `--previous`, the target must be an existing stable semantic-version tag such as `2.2.8` or `v2.2.8`. Each repository independently selects its greatest existing lower stable version in the same major/minor series. A previous `2.2.7` can be selected for `2.2.8`; `2.1.9`, `2.3.0`, and prerelease tags are excluded. Optional `v` prefixes are supported. Missing previous versions or ambiguous tags for the same version fail that component rather than guessing.

`--group`, `--all-groups`, and repeatable `--only PROJECT` select components from `release.repositories`. Interactive group choices come from those project paths. A group matches the exact full namespace, excluding nested subgroups and similarly named groups. Selection happens before GitLab requests, so unselected repositories are not scanned. `--concurrency` and `--timeout` override `release.concurrency` and `release.timeout`, which default to `8` and `2m`.

In an interactive terminal, omitted source and output choices are prompted for after repository selection. The root `yas` operation menu also offers release scanning. Non-interactive scans require a target and either `--from` or `--previous`. The default output is `release-notes-<version>.yaml`; `--output` accepts a new `.yaml` or `.yml` path and creates missing parent directories. Existing files are never overwritten, and the destination is reserved before scanning. The file uses permissions `0600`.

The YAML document contains:

- `schema_version` and editable `release` metadata, including a blank summary and `draft: true`.
- `scan` timestamps, execution settings, and changed/unchanged/failed totals.
- Ordered `components` with baseline/target references, their resolved full commit SHAs, status, and any error.
- One editable `commits` list per component, initialized with the original commit titles/messages, authors, full SHAs, and links.
- Empty `upgrade_notes` and `known_issues` sections for editorial input.

Every `components[].commits[]` entry includes optional numeric identifiers and a blank `remarks` string for a manually written description of the change:

```yaml
change_request: null
incident: null
remarks: ""
```

`null` means no linked record. Fill the identifiers when relevant and describe the change's impact in `remarks`, for example:

```yaml
change_request: 256
incident: 128
remarks: "Restores checkout after payment failures."
```

The identifiers and remarks are not fetched or inferred automatically.

Review and complete release information directly under `commits`, keeping each commit's SHA and link for provenance. Add the release summary, upgrade instructions, and known issues before publishing.

References are resolved once per component, then GitLab's paginated commit listing is read for the pinned `BASELINE_SHA..TARGET_SHA` range. Every page is collected before a successful result is reported. A component is `changed` when this range contains new commits and `unchanged` when it contains none, even if the endpoint SHAs differ. This scan retrieves commit metadata and does not request file diffs.

A missing/inaccessible tag, denied request, timed-out listing, or incomplete pagination is a failed component. Successful and failed components are both saved in the YAML draft in selected configuration order; any component failure returns exit code `1`. Validation errors before scanning do not create a draft.

`--format text|json` controls the terminal summary; JSON stdout is an array of component results. The YAML output path is announced on stderr, including with `--no-progress`. Release scans do not create the batch HTML operation report. `--dry-run` is rejected because scanning already performs read-only GitLab requests and writes the requested draft.

## Render release notes as HTML

After completing the editable YAML, generate a standalone HTML document from that file:

```bash
# Default: writes drafts/release-notes-2.2.8.html beside the input:
yas release render --input drafts/release-notes-2.2.8.yaml
# Choose the output file and initial grouping:
yas release render --input drafts/release-notes-2.2.8.yaml --output releases/2.2.8.html --group-by change-request
```

Rendering uses only the input YAML; it requires no configuration file, GitLab token, Docker, or network connection. The YAML is not rewritten. The HTML includes the completed commit information, identifiers, remarks, release metadata, unchanged components, and any scan failures. It displays the input's `release.draft` value as provided; rendering does not clear that flag.

Open the HTML in a browser to read it offline or print it. A dropdown switches between all five views without generating another file. `--group-by` selects the initial view and defaults to `repository`:

| Grouping | Commits grouped by |
| --- | --- |
| `repository` | Component project |
| `change-request` | Numeric `change_request` identifier |
| `incident` | Numeric `incident` identifier |
| `remarks` | Exact `remarks` string, including case and leading/trailing whitespace |
| `author` | Commit author |

Missing identifiers, remarks, and authors have explicit buckets so those commits remain visible. Remarks are not trimmed, case-folded, or otherwise normalized when grouping.

Without `--output`, HTML replaces the input filename's `.yaml` or `.yml` extension with `.html` in the same directory. An explicit relative output path is resolved from the current working directory. Existing files are never overwritten, and the new HTML file uses permissions `0600`.

In an interactive terminal, `yas release render` prompts for an omitted input path, the initial grouping, and an output path with its default. The root `yas` menu also offers **Generate HTML release notes** and can be opened without configuration.

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

release:
  concurrency: 8
  timeout: 2m
  repositories:
    - project: my-group/service-a
    - project: my-group/service-d
```

The lists are independent: this example tags services A and B, builds services A and C, and scans release changes for services A and D. Lists for unused operations may be empty. Running an operation with an empty applicable repository list fails clearly.

### Fields and defaults

| Field | Default / behavior |
| --- | --- |
| `version` | Required; must be `1` |
| `gitlab.url` | `https://gitlab.com` |
| `gitlab.insecure_skip_verify` | `false`; enable explicitly to skip GitLab TLS certificate verification |
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
| `release.concurrency` | `8`; must be positive |
| `release.timeout` | `2m`; positive time limit for each component scan |
| `release.repositories[].project` | Required project ID or full namespace/project path for release scanning |

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
| `--concurrency N` | Batch commands and release scans; maximum simultaneous repository jobs |
| `--only PROJECT` | Batch commands and release scans; repeatable selection from that operation's configured list |
| `--report PATH` | Batch commands only; save the HTML report to a new `.html` file at this path |
| `--no-report` | Batch commands only; disable the automatic saved report |

Run `yas tag one --help`, `yas tag batch --help`, `yas build one --help`, `yas build batch --help`, or `yas release scan --help` for command-specific flags. Configuration commands validate or write the fixed file; connection overrides and execution flags apply to their respective operation commands.

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

## Batch reports

`yas tag batch`, `yas tag delete batch`, and `yas build batch` automatically save a standalone HTML report in `./reports/`, including when selected repository jobs fail or the run is canceled. Each default filename contains the operation, a timestamp, and a unique suffix. Open the file in a browser to read, share, or print/save it as a PDF; it needs no internet connection or additional software to render beyond the browser. Single-repository commands do not create reports.

Each report includes:

- The operation, application version, GitLab server, and execution or dry-run mode.
- Start and finish timestamps with time zones, total elapsed time, concurrency, and the per-job timeout.
- Total, passed, failed, skipped, and planned counts.
- One row per selected repository job in configuration order, showing its outcome, exact status, source reference, resolved commit SHA when available, destination tag or image, duration, and failure details. Builds also show separate build/save outcomes, archive paths, and save errors when saving is requested.

`created`, `deleted`, and `built` count as **passed**. `skipped` and `planned` remain separate: a skipped job made no change, and a planned dry-run job did not execute the operation. Dry-run reports describe the checks performed and do not prove that a later build or tag write will succeed. Reports contain result details rather than raw Docker logs, configuration contents, environment variables, command-line arguments, or build arguments.

```bash
# Save automatically in ./reports/:
yas build batch --tag v1.2.0
# Choose a new report file; missing parent directories are created:
yas tag batch --branch release/1.2 --report reports/release-1.2.html
# Keep JSON stdout for scripts while also saving the HTML report:
yas build batch --tag v1.2.0 --format json > results.json
# Disable the saved report for this invocation:
yas tag delete batch --tag old-release --no-report
```

`--report` accepts an absolute path or a path relative to the current working directory and requires a `.html` extension. Existing files are never overwritten. `--report` and `--no-report` cannot be used together when reporting is disabled. The saved report path is printed to stderr, including with `--no-progress`, so text/JSON stdout retains its existing format.

The report destination is reserved before jobs run; an unavailable destination stops execution before any repository operation starts. Configuration, validation, or prompt errors before execution do not create reports. Once jobs run, their results are saved before terminal output or the final failure is returned. A report-writing failure returns exit code `1` even if the repository operations succeeded; completed operations are not rolled back.

## Dry runs, results, and failures

```bash
yas tag batch --branch release/1.2 --dry-run
yas build batch --tag v1.2.0 --dry-run --format json
```

A dry run still requires the configured token and contacts GitLab. Tagging checks source existence, branch reachability where requested, and existing tag conflicts. Building resolves source refs but does not download archives, inspect Dockerfiles or build contexts remotely, contact Docker, or prove that a future build will succeed. A tagging dry run also cannot prove permission to create a new protected tag because it performs no write.

Operations report one result per repository job in input/configuration order, even when jobs finish in a different order. Status values are `created`, `deleted`, `built`, `skipped`, `planned`, and `failed`. Results include the resolved commit SHA when available; JSON also contains operation/source/target fields, job duration, a `message` explaining confirmed outcomes, and an `error` for failures.

```bash
yas build batch --tag v1.2.0 --non-interactive --format json > results.json
```

JSON results go to standard output as an array. Prompts, errors, and labeled Docker logs go to standard error, keeping standard output usable for scripts. Text output includes a table and status totals.

For tag deletion, a missing-tag response is reported as `skipped` only after the repository is confirmed accessible; the explanation names the absent tag and repository. If verification fails after the initial tag lookup, the job is `failed` because absence cannot be confirmed, and no deletion was attempted. If verification fails after a deletion request returned `404`, the message states that the request was sent and its result is unconfirmed. A denial while looking up a tag is distinguished from a denial of the deletion request itself. Access denial can involve repository permissions, token permissions, or protected-tag rules; the error does not assume which rule caused it. Other unconfirmed write outcomes also preserve uncertainty instead of claiming that no change occurred. Failures before the build workflow begins mark the build as `not-run`; Docker command failures identify the image, command, and exit code and refer to the labeled Docker logs for details.

Jobs run through a bounded worker pool. A repository failure does not stop unrelated repositories, and a failed batch can have successful tags or images. There is no rollback. Re-running a tagging batch skips tags already pointing at the requested commit; successful Docker builds can replace an existing local image tag normally.

The per-job timeout begins when a worker starts that job. It includes GitLab resolution, retry delays, source download/extraction, Docker builds, and requested image saves as applicable; queue waiting, configuration loading, and interactive prompts are outside this timeout. `Ctrl+C`/SIGINT and SIGTERM cancel outstanding work and running Docker commands. Already completed tags, images, and archives remain; temporary downloaded source and incomplete archives are cleaned up.

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
