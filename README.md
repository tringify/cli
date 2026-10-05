# Tringify CLI

The `tringify` command builds and ships Tringify themes and apps from your terminal. For themes: create one from the starter, preview it with sample content, add it to a development store, and publish versions to your theme listing. For apps: start a project from the app starter, keep the app's configuration in `tringify.app.json`, and release versions.

## Install

macOS and Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/tringify/cli/main/install.sh | sh
```

The script downloads the release for your platform, checks it against the published `SHA256SUMS`, and installs `tringify` in `~/.local/bin`. Set `TRINGIFY_CLI_VERSION` to install a specific release.

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/tringify/cli/main/install.ps1 | iex
```

The script checks the download against `SHA256SUMS`, installs `tringify.exe` in `%LOCALAPPDATA%\Programs\tringify` and adds that folder to your `PATH`. Set `TRINGIFY_CLI_VERSION` to install a specific release, or `TRINGIFY_CLI_BIN` to install somewhere else.

Builds are available for macOS (Apple silicon and Intel), Linux (x86-64 and ARM64), and Windows (x86-64; Windows on Arm runs it too). To build from source: `go install github.com/tringify/cli/cmd/tringify@latest`.

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
tringify theme diff --listing LISTING_ID       # what changes compared with the published version
tringify theme publish --listing LISTING_ID --version 1.0.0 --notes "First release"
```

Create the listing itself (name, category, pricing, support details) in the [Developer Portal](https://dev.tringify.com) under **Themes**.

### Apps

```sh
tringify app create --name "My app" --type standard --distribution private
                                        # prints the app ID, client ID and client secret (shown once)
tringify app init --app APP_ID          # project from the app starter, with tringify.app.json
# edit tringify.app.json: scopes, webhook URL, admin URL, events
tringify app config push                # shows what changes, then replaces the draft
tringify app webhook test               # sends a signed app.test webhook
tringify app install-link --store my-store.mytringify.com
```

A marketplace app then releases versions; a private app sends updates to stores that installed it:

```sh
tringify app release --bump minor --notes "First release"   # marketplace
tringify app publish 1.0.0                                   # once approved
tringify app release --all --notes "New settings page"       # private
```

`tringify.app.json` holds everything a release carries except the listing (name, descriptions, screenshots, pricing), which stays in the Developer Portal. Pushing replaces the whole draft: a field you delete from the file is cleared. Stores only see a change once you release a version and publish it.

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
| `theme dev --store ID [--storefront ID] [--theme ID] [--with-demo] [DIR]` | Adds the theme to the store as an unpublished theme, then builds, validates and syncs it every time you save. The theme it uses is remembered in `.tringify/dev.json`; add `.tringify/` to your `.gitignore`. The store's published theme is never changed. `--with-demo` also adds the products and collections from `demo/catalog.json` to a development store. Products and collections the store already has are skipped, and only the images of the ones it adds are uploaded. |
| `theme pull --store ID --theme ID [DIR]` | Downloads a store theme's published files into a new directory. Without `--theme` it lists the storefront's themes. |
| `theme pull --listing ID [--version X.Y.Z] [DIR]` | Downloads the exact package a version of your listing was published with (default: the newest published version) into a new directory. |
| `theme diff --listing ID [--version X.Y.Z] [DIR]` | Packages the theme and lists the files that are added (`+`), changed (`~`) or removed (`-`) compared with a published version of your listing (default: the newest). |
| `store list` | Lists your organization's development stores and their IDs. |
| `store create --name NAME --country CC --currency CUR` | Creates a development store and waits until it is ready. `--subdomain`, `--timezone` and `--billing-currency` are optional. |
| `theme listings` | Lists your organization's theme listings and their IDs. |
| `theme publish --listing ID --version X.Y.Z [--notes TEXT] [--breaking] [--install-store ID] [--yes] [DIR]` | Packages the theme, shows what changes compared with the newest published version, and publishes it as a new version of the listing once you confirm. Pass `--yes` to skip the question, for example in CI. |
| `app list` | Lists your organization's apps and their IDs. |
| `app create --name NAME --type standard\|sales_channel --distribution private\|marketplace [--category ID --subcategory ID] [--description TEXT] [--json]` | Creates an app and prints its app ID, client ID and client secret. The secret is shown only in this output; to issue a new one, regenerate it in the Developer Portal. Sales channel apps need a category and subcategory, which set the channel type. With `--json` the result is JSON, for scripts and piping into a secret store. |
| `app init --app ID [DIR]` | Creates a project from [app-starter](https://github.com/tringify/app-starter) in a new directory (default: the app's slug), writes the app's configuration to `tringify.app.json`, sets the app and client IDs, and creates `.dev.vars` for local development. Add the client secret from `app create` and the signing secret from `app webhook rotate-key` to it. |
| `app config pull [--app ID] [--file PATH] [--yes]` | Writes the app's draft configuration to `tringify.app.json`. If the file exists and differs, shows the differences and asks first. |
| `app config push [--file PATH] [--yes]` | Shows the fields that differ from the draft and, once you confirm, replaces the draft with the file. Unknown fields are refused. The saved values are written back to the file when the platform normalizes them, for example sorting scopes. |
| `app install-link [--store DOMAIN] [--draft] [--expires 7d] [--uses 1]` | Prints a link that installs the app on a store. A private app installs its current draft; a marketplace app installs its published version, or with `--draft` its draft (development and transfer stores of your organization only). |
| `app release [--bump patch\|minor\|major] [--store ID]... [--all] [--notes TEXT]` | Releases the draft. A marketplace app submits it as a new version (`--bump`). A private app sends it as an update to the installed stores you name, or `--all` of them; each store accepts it in its admin. Refuses while `tringify.app.json` differs from the draft. |
| `app versions` | Lists the app's versions and their review status, or for a private app, the updates sent and how many stores accepted them. |
| `app publish VERSION` | Publishes an approved version so stores can install it or update to it. |
| `app webhook test` | Sends a signed `app.test` webhook to the app's webhook URL and reports what your endpoint answered. |
| `app webhook rotate-key [--json]` | Issues a new webhook signing secret and prints it once. Deliveries signed with the previous secret stop verifying immediately, so update your app right after. |
| `app deliveries [--status STATUS] [--limit N]` | Lists recent webhook deliveries with their status, attempts and your endpoint's last answer. |
| `version` | Prints the CLI version. |

Edit sections in `src/sections/<name>/` as `body.html`, `style.css` and `schema.json`, with shared styles in `src/_shared.css`, or write `sections/*.vasc` files directly. Keep `src/.generated-sections.json` in version control: it records which section files the build generated, so removing a section's source also removes its compiled file safely, and a compiled file you edited by hand is never deleted.

If you used the `tringify-theme` command from the theme tools, every one of its commands is available as `tringify theme <command>` with the same options, rules and output, and works on the same theme files.

`theme publish` only runs from a clean git work tree. The version's release notes record the commit it was built from, so every published version can be traced to its source. With `--install-store`, the new version is also installed, unpublished, into one of your organization's development stores.

## Signing in and access

`tringify login` opens the Tringify sign-in page in your browser. You choose the developer organization (or, with `--store`, the store) and approve the access the CLI asks for:

| Login | Access requested |
| --- | --- |
| Developer organization | View and edit your organization's themes and apps, and list and create its development stores (`organization:themes:read`, `organization:themes:write`, `organization:apps:read`, `organization:apps:write`, `organization:dev_stores:read`, `organization:dev_stores:write`) |
| Store | View and edit the store's storefront themes (`store:online_store.storefronts:read`, `store:online_store.storefronts:write`). With `theme dev --with-demo`, also add products and files (`store:products:write`, `store:files:write`). |

The CLI never receives more access than your own role in that organization or store, and if your role changes, the CLI's access changes with it. It cannot change your live theme, delete stores or apps, create listings, change pricing, or read or regenerate an app's client secret. If you signed in before app or development store access was added, run `tringify login` again to use `app` or `store` commands.

Sign-in uses OAuth with PKCE and a one-time listener on `127.0.0.1`, so your password never passes through the CLI. Access tokens last 15 minutes and are refreshed automatically; refresh tokens rotate on every use. Tokens are kept only in your system keychain: the macOS Keychain, Windows Credential Manager, or the Secret Service on Linux (GNOME Keyring, KWallet). Without one, sign-in stops and says so.

You can see and disconnect the CLI's access at any time in your Tringify account under **Connected tools**, or run `tringify logout`.

Working over SSH? Set `TRINGIFY_NO_BROWSER=1` and open the printed sign-in URL on your own machine; the browser must be able to reach `127.0.0.1` on the machine running the CLI (for example through SSH port forwarding).

## Use with AI agents

The same theme operations are available to MCP clients on the Tringify developer and store MCP endpoints, with the same access model. See the [Tringify developer documentation](https://dev-docs.tringify.com).

## Contributing

Bug reports and pull requests are welcome. Run `go test ./...` before sending a change.

## License

[MIT](LICENSE)
