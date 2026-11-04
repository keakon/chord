# Quickstart

Have a model API key or an account with Codex access ready. See [supported accounts and APIs](./model-choice.md#supported-accounts-and-apis) for access requirements.

## 1. Install

### Homebrew (macOS and Linux)

With [Homebrew](https://brew.sh) installed, run:

```bash
brew install keakon/tap/chord
```

Homebrew automatically adds the [Chord tap](https://github.com/keakon/homebrew-tap) and installs the release binary for macOS or Linux on ARM64 or x86_64. It verifies the package's SHA256 and installs Bash, Zsh, and Fish completions. Go is not required; the formula does not fall back to compiling from source.

To upgrade:

```bash
brew update
brew upgrade chord
```

### Download a binary

Download the archive for your OS and architecture from [GitHub Releases](https://github.com/keakon/chord/releases), extract it, and put `chord` on your `PATH`. These binaries do not require Go.

### Install with Go

With Go 1.27.0 or newer:

```bash
go install github.com/keakon/chord/cmd/chord@latest
```

### Verify the installation

After installing with any of these methods, run:

```bash
chord --version
```

### If macOS blocks the downloaded binary

On macOS, a binary downloaded manually from GitHub Releases may be blocked on first run because it came from the internet and is not notarized. If that happens, run:

```bash
xattr -dr com.apple.quarantine /path/to/chord
chmod +x /path/to/chord
/path/to/chord --version
```

If macOS still blocks it, add a local ad-hoc signature:

```bash
codesign --force --sign - /path/to/chord
```

Replace `/path/to/chord` with the actual installed path, such as `/usr/local/bin/chord`.

## 2. First run

Open your project in an interactive terminal and start Chord:

```bash
cd my-project
chord
```

If `config.yaml` is missing, the setup wizard offers two ways to connect:

- **API key**: enter your provider's full API URL, model name, and key. You can also enter a proxy URL if needed.
- **Codex OAuth**: follow the sign-in prompts without entering an API key manually.

The wizard creates a minimal `config.yaml` and, when needed, `auth.yaml`, then shows where it saved them. It reuses matching credentials when possible. Chord also creates the project's `.chord/` directory as needed.

See [More configuration options](#more-configuration-options) for API URL formats, manual setup, and setup without an interactive terminal.

## 3. Send your first task

Describe what you want and press `Enter`. For example, have it read through your project first:

```text
Explain this project's main modules and how to run its tests. Do not change any files yet.
```

Read its answer. With the default configuration, Chord edits files and runs commands directly; only file deletions through the `delete` tool ask first. When a confirmation appears, inspect the pending action before deciding whether to allow it. To require approval for more actions, see [Permissions & Safety](./permissions-and-safety.md). Once you are comfortable, give Chord a specific change: it edits code and runs tests, and you review the diff in the terminal.

To quit, press `Esc` to enter Normal mode, then `q`; alternatively, press `Ctrl+C` twice within 2 seconds. Next time, run this from your project directory:

```bash
chord --continue
```

This resumes your most recent session. See [Sessions](./usage.md#sessions) to choose another session or import external history.

## If requests fail

If an API-key configuration cannot reach the model, exit Chord and run:

```bash
chord doctor models
```

Resolve authentication or connection errors before running `chord` again. See [Troubleshooting](./troubleshooting.md) for help.

## More configuration options

### Configure a model from the catalog

If you have an API key, you can skip the setup wizard:

```bash
export OPENAI_API_KEY="your-api-key"
chord config add openai/gpt-6.1-sol
chord
```

The add command creates your config and fills the documented API connection.
For another model, run `chord config add` without arguments and pick from the
verified catalog. It tells you which API key variable to set. Existing providers keep their
endpoint and credentials.

Use `chord doctor config` to check the configuration and the catalog snapshot
in effect. See [model configuration](./model-configs.md) for custom gateways
and other connection options.

### Manual configuration and setup requirements

The wizard accepts API URLs ending in `/responses`, `/messages`, or `/chat/completions`, or a Gemini version root such as `/v1beta/`. It recommends starter provider/model defaults from that suffix. See [Configuration & Auth](./configuration.md) for endpoint formats, credentials, and model pools. To write configuration yourself, start with an [example](./examples/index.md).

Setup needs a controlling terminal: redirected stdin alone does not disable the wizard as long as Chord can still open the controlling TTY, and without one Chord exits with an initialization error instead of waiting for input. For setup without an interactive terminal, see [Troubleshooting](./troubleshooting.md#startup-failures).

## Choose your next step

- [Permissions & Safety](./permissions-and-safety.md): choose which actions need approval.
- [Usage](./usage.md): everyday controls and session recovery.
- [Model pools](./configuration.md#model-pools-selecting-providermodel): configure multiple models so Chord can automatically try another model when a request fails.
- [Worktrees](./usage.md#worktrees): work on parallel tasks in separate working directories.
- [Customization](./customization.md): configure multiple agents, skills, and external tools.
