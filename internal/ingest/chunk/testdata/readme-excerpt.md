CALM ships as a single static binary beside your workload. This preamble
sits before any heading and stays titled by the source label.

# Install

Grab a release or build from source.

## macOS

Install the toolchain first:

```sh
# this hash line must never become a heading
brew install go-task
task build
```

Then verify the binary landed:

    ./bin/calm --version
    # indented code block, also never a heading

## Linux

- download the tarball
- unpack into `/usr/local/bin`
- run the smoke check

Configuration
-------------

The setext heading above must open its own section. Namespaces are declared
in YAML and loaded at startup.

```yaml
namespaces:
  - name: default
    api_key: "[env:CALM_KEY]"
```

Ordered steps also count as list structure:

1. create a namespace
2. mint a session
