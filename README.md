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

The CLI is a single binary and needs nothing else installed: no Python, Node.js or other runtime. The first time you check, package or preview a theme, it downloads the theme validator (`themecheck`) and preview renderer (`theme-preview-render`) from the [theme tools](https://github.com/tringify/theme-tools) release for your platform, verifies them against the release's `SHA256SUMS`, and keeps them in your user cache directory:

| System | Location |
| --- | --- |
| macOS | `~/Library/Caches/tringify/theme-tools` |
| Linux | `$XDG_CACHE_HOME/tringify/theme-tools` (usually `~/.cache/tringify/theme-tools`) |
| Windows | `%LocalAppData%\tringify\theme-tools` |

The CLI looks for a newer theme tools release at most once a day and keeps working offline with the one it has. To control this:

| Variable | Effect |
| --- | --- |
| `TRINGIFY_THEME_TOOLS_VERSION` | Use a specific theme tools release, for example `v0.1.0`. |
| `TRINGIFY_THEME_TOOLS_HOME` | Keep the two binaries in this directory instead of the cache. |
| `TRINGIFY_THEME_CHECK`, `TRINGIFY_THEME_PREVIEW` | Use a `themecheck` or `theme-preview-render` you already have. The `--checker` and `--renderer` options do the same for one command. |

## Quick start

```sh
tringify login                       # choose your developer organization
tringify theme init my-theme         # create a theme from the starter
cd my-theme
tringify theme preview               # live preview with sample content
tringify theme check                 # validate
```

Work against a real store with your own products:

```sh
tringify store create --name "Theme Lab" --country IN --currency INR
tringify theme dev --store STORE_ID  # syncs every saved change into an unpublished theme
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
| `login --store` | Signs in to a store. `theme push`, `theme dev` and `theme pull` do this for you when needed. |
| `logout` | Signs out of the developer organization and revokes the CLI's access. `--store ID` signs out of one store; `--all` signs out everywhere. |
| `whoami` | Shows the organization and stores you are signed in to and the access each login has. |
| `theme init [DIR] [--name NAME] [--from SOURCE]` | Creates a theme from [theme-starter](https://github.com/tringify/theme-starter), or from a local theme directory with `--from`, then builds and validates it. `DIR` must not exist; nothing is created if validation fails. |
| `theme preview [DIR] [--port 9292] [--preset NAME] [--host ADDRESS]` | Serves a local preview with sample content at `127.0.0.1:9292` and rebuilds when you edit sources. A failed edit shows the error and keeps the last good page. |
| `theme build [DIR]` | Compiles `src/sections/<name>/` into `sections/<name>.vasc` and updates the section list in `theme.json`. |
| `theme check [DIR] [--mode sealed\|development]` | Validates the compiled theme, as it is, with the same rules used on upload. Run `theme build` first after editing `src/`. |
| `theme package [DIR] [OUTPUT]` | Builds in a temporary copy, validates, and writes an upload-ready ZIP (default `dist/<theme>.zip`; `--output FILE` also works). A failed validation never replaces an existing package. |
| `theme context [DIR] --page PAGE [--entity HANDLE] [--preset NAME]` | Prints the exact sample data (CTX) the preview gives a page, such as `home`, `product`, `collection` or `page`. |
| `theme contract` | Prints the theme author contract as JSON: CTX roots and fields, editor setting types and hosted form actions. |
| `theme push --store ID [--storefront ID] [DIR]` | Packages the theme and adds it to the store as a new, unpublished theme. The live theme is not changed. |
| `theme dev --store ID [--storefront ID] [--theme ID] [--with-demo] [DIR]` | Adds the theme to the store as an unpublished theme, then builds, validates and syncs it every time you save. The theme it uses is remembered in `.tringify/dev.json`; add `.tringify/` to your `.gitignore`. The store's published theme is never changed. `--with-demo` also adds the products and collections from `demo/catalog.json` to a development store, with their images; products the store already has are skipped. |
| `theme pull --store ID --theme ID [DIR]` | Downloads a store theme's published files into a new directory. Without `--theme` it lists the storefront's themes. |
| `store list` | Lists your organization's development stores and their IDs. |
| `store create --name NAME --country CC --currency CUR` | Creates a development store and waits until it is ready. `--subdomain`, `--timezone` and `--billing-currency` are optional. |
| `theme listings` | Lists your organization's theme listings and their IDs. |
| `theme publish --listing ID --version X.Y.Z [--notes TEXT] [--breaking] [--install-store ID] [DIR]` | Packages the theme and publishes it as a new version of the listing. |
| `version` | Prints the CLI version. |

Edit sections in `src/sections/<name>/` as `body.html`, `style.css` and `schema.json`, with shared styles in `src/_shared.css`, or write `sections/*.vasc` files directly. Keep `src/.generated-sections.json` in version control: it records which section files the build generated, so removing a section's source also removes its compiled file safely, and a compiled file you edited by hand is never deleted.

If you used the `tringify-theme` command from the theme tools, every one of its commands is available as `tringify theme <command>` with the same options, rules and output, and works on the same theme files.

`theme publish` only runs from a clean git work tree. The version's release notes record the commit it was built from, so every published version can be traced to its source. With `--install-store`, the new version is also installed, unpublished, into one of your organization's development stores.

## Signing in and access

`tringify login` opens the Tringify sign-in page in your browser. You choose the developer organization (or, with `--store`, the store) and approve the access the CLI asks for:

| Login | Access requested |
| --- | --- |
| Developer organization | View and edit your organization's themes, and list and create its development stores (`organization:themes:read`, `organization:themes:write`, `organization:dev_stores:read`, `organization:dev_stores:write`) |
| Store | View and edit the store's storefront themes (`store:online_store.storefronts:read`, `store:online_store.storefronts:write`). With `theme dev --with-demo`, also add products and files (`store:products:write`, `store:files:write`). |

The CLI never receives more access than your own role in that organization or store, and if your role changes, the CLI's access changes with it. It cannot change your live theme, delete stores, create listings, or change pricing. If you signed in before development store access was added, run `tringify login` again to use `store` commands.

Sign-in uses OAuth with PKCE and a one-time listener on `127.0.0.1`, so your password never passes through the CLI. Access tokens last 15 minutes and are refreshed automatically; refresh tokens rotate on every use. Tokens are kept in your system keychain (macOS Keychain, Windows Credential Manager, or the Secret Service on Linux). Where no keychain is available, they are stored in `~/.config/tringify/credentials.json`, readable only by you. Set `TRINGIFY_CREDENTIALS_STORE=file` to use the file on purpose.

You can see and disconnect the CLI's access at any time in your Tringify account under **Connected tools**, or run `tringify logout`.

Working over SSH? Set `TRINGIFY_NO_BROWSER=1` and open the printed sign-in URL on your own machine; the browser must be able to reach `127.0.0.1` on the machine running the CLI (for example through SSH port forwarding).

## Use with AI agents

The same theme operations are available to MCP clients on the Tringify developer and store MCP endpoints, with the same access model. See the [Tringify developer documentation](https://dev-docs.tringify.com).

## Contributing

Bug reports and pull requests are welcome. Run `go test ./...` before sending a change.

## License

[MIT](LICENSE)
