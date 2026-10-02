# Tringify CLI

The `tringify` command builds and ships Tringify storefront themes from your terminal: create a theme from the starter, preview it with sample content, add it to a development store, and publish versions to your theme listing.

## Install

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/tringify/cli/main/install.sh | sh
```

The script downloads the release for your platform, checks it against the published `SHA256SUMS`, and installs `tringify` in `~/.local/bin`. Set `TRINGIFY_CLI_VERSION` to install a specific release.

Windows: download `tringify-windows-amd64.zip` from [Releases](https://github.com/tringify/cli/releases), check it against `SHA256SUMS`, and put `tringify.exe` on your `PATH`.

Builds are available for macOS (Apple silicon and Intel), Linux (x86-64 and ARM64), and Windows (x86-64). To build from source: `go install github.com/tringify/cli/cmd/tringify@latest`.

The theme commands use the [Tringify theme tools](https://github.com/tringify/theme-tools), which need Python 3.10 or newer. If `tringify-theme` is not installed, the CLI downloads the theme tools release for your platform on first use, verifies its checksum, and keeps it in `~/.tringify/theme-tools`.

## Quick start

```sh
tringify login                       # choose your developer organization
tringify theme init my-theme         # create a theme from the starter
cd my-theme
tringify theme preview               # live preview with sample content
tringify theme check                 # validate
tringify theme push --store STORE_ID # add it to a development store
```

When the theme is ready, commit it and publish a version to your listing:

```sh
git init && git add . && git commit -m "First version"
tringify theme listings
tringify theme publish --listing LISTING_ID --version 1.0.0 --notes "First release"
```

Create the listing itself (name, category, pricing, support details) in the [Developer Portal](https://dev.tringify.com) under **Themes**.

## Commands

| Command | What it does |
| --- | --- |
| `login` | Signs in to a developer organization in your browser. |
| `login --store` | Signs in to a store. `theme push` does this for you when needed. |
| `logout` | Signs out of the developer organization and revokes the CLI's access. `--store ID` signs out of one store; `--all` signs out everywhere. |
| `whoami` | Shows the organization and stores you are signed in to and the access each login has. |
| `theme init [DIR] [--name NAME]` | Creates a theme from [theme-starter](https://github.com/tringify/theme-starter). |
| `theme preview [DIR]` | Serves a local preview that rebuilds when you edit sources. |
| `theme check [DIR]` | Validates the theme with the same rules used on upload. |
| `theme package [DIR] [--output FILE]` | Writes an upload-ready ZIP (default `dist/<theme>.zip`). |
| `theme push --store ID [--storefront ID] [DIR]` | Packages the theme and adds it to the store as a new, unpublished theme. The live theme is not changed. |
| `theme listings` | Lists your organization's theme listings and their IDs. |
| `theme publish --listing ID --version X.Y.Z [--notes TEXT] [--breaking] [--install-store ID] [DIR]` | Packages the theme and publishes it as a new version of the listing. |
| `version` | Prints the CLI version. |

`theme publish` only runs from a clean git work tree. The version's release notes record the commit it was built from, so every published version can be traced to its source. With `--install-store`, the new version is also installed, unpublished, into one of your organization's development stores.

## Signing in and access

`tringify login` opens the Tringify sign-in page in your browser. You choose the developer organization (or, with `--store`, the store) and approve the access the CLI asks for:

| Login | Access requested |
| --- | --- |
| Developer organization | View and edit your organization's themes (`organization:themes:read`, `organization:themes:write`) |
| Store | View and edit the store's storefront themes (`store:online_store.storefronts:read`, `store:online_store.storefronts:write`) |

The CLI never receives more access than your own role in that organization or store, and if your role changes, the CLI's access changes with it. It cannot change your live theme, create listings, or change pricing.

Sign-in uses OAuth with PKCE and a one-time listener on `127.0.0.1`, so your password never passes through the CLI. Access tokens last 15 minutes and are refreshed automatically; refresh tokens rotate on every use. Tokens are kept in your system keychain (macOS Keychain, Windows Credential Manager, or the Secret Service on Linux). Where no keychain is available, they are stored in `~/.config/tringify/credentials.json`, readable only by you. Set `TRINGIFY_CREDENTIALS_STORE=file` to use the file on purpose.

You can see and disconnect the CLI's access at any time in your Tringify account under **Connected tools**, or run `tringify logout`.

Working over SSH? Set `TRINGIFY_NO_BROWSER=1` and open the printed sign-in URL on your own machine; the browser must be able to reach `127.0.0.1` on the machine running the CLI (for example through SSH port forwarding).

## Use with AI agents

The same theme operations are available to MCP clients on the Tringify developer and store MCP endpoints, with the same access model. See the [Tringify developer documentation](https://dev-docs.tringify.com).

## Contributing

Bug reports and pull requests are welcome. Run `go test ./...` before sending a change.

## License

[MIT](LICENSE)
