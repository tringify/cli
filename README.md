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

The CLI is a single binary and needs nothing else installed: no Python, Node.js or other runtime. The first time you check, package or preview a theme, or check or preview app blocks, it downloads the theme validator (`themecheck`) and preview renderer (`theme-preview-render`) from the [theme tools](https://github.com/tringify/theme-tools) release for your platform, verifies them against the release's `SHA256SUMS`, and keeps them in your user cache directory.

| System | Location |
| --- | --- |
| macOS | `~/Library/Caches/tringify/theme-tools` |
| Linux | `$XDG_CACHE_HOME/tringify/theme-tools` (usually `~/.cache/tringify/theme-tools`) |
| Windows | `%LocalAppData%\tringify\theme-tools` |

The CLI looks for a newer theme tools release at most every 15 minutes and keeps working offline with the one it has. To control this:

| Variable | Effect |
| --- | --- |
| `TRINGIFY_THEME_TOOLS_VERSION` | Use a specific theme tools release, for example `v0.1.0`. |
| `TRINGIFY_THEME_TOOLS_HOME` | Keep the theme tools in this directory instead of the cache. |
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
tringify app block preview              # the example block, blocks/instalments.vasc, in the starter theme
tringify app config push                # checks the blocks, shows what changes, then replaces the draft
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

### App blocks

App blocks are Vascula blocks merchants place in their theme. Each block's entry in `storefront_blocks` gives its `key`, `name`, `description`, `mode`, `pages` and, for hosted surfaces, `surface_key`; its source lives in its own file, named by `source_file`:

```json
"storefront_blocks": [
  {
    "key": "instalments",
    "name": "Instalments",
    "description": "Shows the product price split into equal interest-free payments.",
    "mode": "template",
    "pages": ["product"],
    "source_file": "blocks/instalments.vasc"
  }
]
```

A `template` block is markup only: the storefront renders it into the page and caches it with the page, so it may read only `shop`, `routes`, `product` and `collection`. A `hosted_surface` block also opens one of the app's hosted surfaces after its markup and may read `cart` too. A block that reads `product` or `collection` data lists that page alone in `pages`. Block markup uses reading elements, links and images, styled with the platform's `tf-ab-*` classes, which take the theme's font and colours; scripts, styles, event handlers, forms, ids and data attributes are refused. `tringify theme contract` lists the exact elements, attributes and classes.

```sh
tringify app block check                # FILE:LINE: problems, as the platform would refuse them
tringify app block context --page product   # the data each block receives on a product page
tringify app block preview              # the blocks in the starter theme, rebuilt as you edit
tringify app block preview --theme ../my-theme --page collection
```

`app config push` and `app release` run the check first and stop if it finds a problem.

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
| `theme dev --store ID [--storefront ID] [--theme ID] [--with-demo] [DIR]` | Adds the theme to the store as an unpublished theme, then builds, validates and syncs it every time you save. The theme it uses is remembered in `.tringify/dev.json`; add `.tringify/` to your `.gitignore`. With `--theme` it syncs into that unpublished theme instead, such as a duplicate made in Store Admin; the default theme is refused. The store's published theme is never changed. `--with-demo` also adds the products and collections from `demo/catalog.json` to a development store. Products and collections the store already has are skipped, and only the images of the ones it adds are uploaded. |
| `theme pull --store ID --theme ID [DIR]` | Downloads a store theme's published files into a new directory. Without `--theme` it lists the storefront's themes. |
| `theme pull --listing ID [--version X.Y.Z] [DIR]` | Downloads the exact package a version of your listing was published with (default: the newest published version) into a new directory. |
| `theme diff --listing ID [--version X.Y.Z] [DIR]` | Packages the theme and lists the files that are added (`+`), changed (`~`) or removed (`-`) compared with a published version of your listing (default: the newest). |
| `store list` | Lists your organization's development stores and their IDs. |
| `store create --name NAME --country CC --currency CUR` | Creates a development store and waits until it is ready. `--subdomain`, `--timezone` and `--billing-currency` are optional. |
| `theme listings` | Lists your organization's theme listings and their IDs. |
| `theme publish --listing ID --version X.Y.Z [--notes TEXT] [--breaking] [--install-store ID] [--yes] [DIR]` | Packages the theme, shows what changes compared with the newest published version, and publishes it as a new version of the listing once you confirm. Pass `--yes` to skip the question, for example in CI. |
| `app list` | Lists your organization's apps and their IDs. |
| `app create --name NAME [--handle HANDLE] --type standard\|sales_channel --distribution private\|marketplace [--category ID --subcategory ID] [--description TEXT] [--json]` | Creates an app and prints its app ID, handle, client ID and client secret. The handle is made from the name unless you choose it with `--handle`; it is checked before the app is created and cannot change later. The secret is shown only in this output; to issue a new one, run `app secret rotate`. Sales channel apps need a category and subcategory, which set the channel type. With `--json` the result is JSON, for scripts and piping into a secret store. |
| `app categories [--type standard\|sales_channel] [--json]` | Lists the category and subcategory IDs to pass to `app create`. Needs no sign-in. |
| `app init --app ID [DIR]` | Creates a project from [app-starter](https://github.com/tringify/app-starter) in a new directory (default: the app's handle), writes the app's configuration to `tringify.app.json` and its block sources to `blocks/`, adds an example template block (`blocks/instalments.vasc`: the price split into payments, with the number of payments as a merchant setting), sets the app and client IDs, and creates `.dev.vars` for local development. Add the client secret from `app create` and the signing secret from `app webhook rotate-key` to it. |
| `app config pull [--app ID] [--file PATH] [--yes]` | Writes the app's draft configuration to `tringify.app.json` and each block's source to `blocks/<key>.vasc`. If the files exist and differ, shows the differences and asks first. Lists `.vasc` files in `blocks/` that no block uses any more, and leaves them in place. |
| `app config push [--file PATH] [--yes]` | Checks the app blocks, shows the fields that differ from the draft and, once you confirm, replaces the draft with the file and the block sources it names. Unknown fields are refused. The saved values are written back to the files when the platform normalizes them, for example sorting scopes. |
| `app block check [--file PATH]` | Checks the app's blocks with the rules the platform applies when the configuration is pushed, and prints each problem as `FILE:LINE: message`. |
| `app block context [--page PAGE] [--theme DIR] [--entity HANDLE]` | Prints the sample data each block that lists the page receives: the page's data cut to the block's `ctx_needs`. Uses the built-in sample catalog, or a theme's demo content with `--theme`. |
| `app block preview [--theme DIR] [--page PAGE] [--after TYPE] [--port 9292]` | Serves a local preview of the theme (default: the [starter theme](https://github.com/tringify/theme-starter), downloaded as `theme init` does) with the app's blocks that list the page in its app block slot, after the theme block of type `--after` (default `price`). Blocks render as on a store: their mode's data only, the app markup policy, the page's app block budget and the `tf-ab-*` styles. Rebuilds when `tringify.app.json`, a block source or the theme changes. |
| `app install-link [--store DOMAIN] [--draft] [--expires CHOICE] [--uses N]` | Prints a link that installs the app on a store. A private app installs its current draft; a marketplace app installs its published version, or with `--draft` its draft (development and transfer stores of your organization only). The expiry choices and the defaults for both flags come from the Developer API, the same as in the Developer Portal; an unknown `--expires` lists the choices. |
| `app release [--bump patch\|minor\|major] [--store ID\|DOMAIN]... [--all] [--notes TEXT]` | Releases the draft. A marketplace app submits it as a new version (`--bump`): the listing needs an icon, at least one screenshot, a privacy policy URL and a support email or URL (`app listing show` lists what is missing), and a version identical to the published one is refused, since a version is only for configuration or listing changes (code changes go live when you deploy your own server). A private app sends it as an update to the installed stores you name by ID or domain, or with `--all` to every installed store that does not have it yet, in batches of the most stores the API accepts per request; each store accepts it in its admin. Refuses while `tringify.app.json` or a block source differs from the draft, or the draft's blocks fail `app block check`. |
| `app versions` | Lists the app's versions and their review status, or for a private app, the updates sent with their release notes and how many stores accepted each, have it waiting, or got a later version instead. |
| `app publish VERSION` | Publishes an approved version so stores can install it or update to it. |
| `app webhook test` | Sends a signed `app.test` webhook to the app's webhook URL and reports what your endpoint answered. |
| `app webhook rotate-key [--pipe-to COMMAND \| --json]` | Issues a new webhook signing secret and prints it once. For 72 hours, requests also carry a second signature made with the previous secret (`X-Tringify-Signature: t=…,v1=<new>,v1=<previous>`), so your app keeps verifying while you deploy the new one; the output says exactly when signatures made with the previous secret stop. Rotating again during those 72 hours stops signatures made with the oldest secret at once. `--pipe-to` stores it without printing it, as for `app secret rotate`; `--json` includes `previous_key_expires_at` (`null` when the app had no secret before). |
| `app webhook finish-rotation [--app ID] [--yes]` | Stops signing requests with the previous webhook signing secret now instead of when its 72 hours end. Run it once the new secret is deployed everywhere you verify signatures. Asks first; `--yes` skips the question. |
| `app secret rotate [--pipe-to COMMAND \| --json]` | Issues a new client secret and prints it once. For 72 hours the previous secret keeps working too, so your app keeps exchanging codes while you deploy the new one; the output says exactly when the previous secret stops working. Rotating again during those 72 hours stops the oldest secret at once. `--pipe-to` hands the new secret to your host's secret command on its standard input and never prints it, for example `--pipe-to "npx wrangler secret put TRINGIFY_CLIENT_SECRET"` or `--pipe-to "gcloud secrets versions add tringify-client-secret --data-file=-"`. Without it the secret is printed once; `--json` prints it as JSON with `previous_secret_expires_at` (`null` when the app had no secret before). |
| `app secret finish-rotation [--yes]` | Stops accepting the previous client secret now instead of when its 72 hours end. Run it once the new secret is deployed everywhere, or right away if the previous secret was exposed. |
| `app deliveries [--data] [--status STATUS] [--store STORE] [--delivery ID] [--event ID] [--cursor CURSOR] [--limit N]` | Lists webhook deliveries in every region, newest first (lifecycle, or with `--data` store data deliveries), with their status, store, region, attempts and your endpoint's last answer. `--store` takes a store ID, domain or part of its name. Prints the `--cursor` for the next page; a region that does not answer is named on stderr. |
| `app retire [--app ID] [--json]` | Shows whether the app can be retired and why, then opens the app's Danger Zone in the Developer Portal, where you confirm with your email code or passkey. A retired app leaves the App Store and accepts no new installs or versions; stores that installed it keep using it until they uninstall. Retiring cannot be undone. Any app that was ever used is retired, not deleted. |
| `app delete [--app ID] [--json]` | Shows whether the app can be deleted and why, then opens the Danger Zone to confirm with your email code or passkey. Only an app that was never used on any region can be deleted; for any other app it lists the reasons and points to `app retire`. |
| `version` | Prints the CLI version. |

`app retire` and `app delete` print the platform's answer: the app's state, a summary, each reason the action is not available, regions that could not be checked, and the last delete request. They exit with an error when the action you asked for is not available. The CLI cannot confirm either action itself: both need your email code or passkey, which only the Developer Portal asks for. Set `TRINGIFY_NO_BROWSER=1` to only print the Danger Zone link; `--json` prints the answer as JSON and opens nothing.

Theme downloads use a signed HTTPS link that expires after 10 minutes. The CLI downloads immediately, limits ZIP packages to 256 MiB, checks their size, and verifies the digest for listing versions. If a link expires, run the command again to request a fresh one. Downloads send no account credentials to the object store. The directory defaults to the ZIP filename without its extension; pass `DIR` to choose another new directory.

Edit sections in `src/sections/<name>/` as `body.html`, `style.css` and `schema.json`, with shared styles in `src/_shared.css`, or write `sections/*.vasc` files directly. Keep `src/.generated-sections.json` in version control: it records which section files the build generated, so removing a section's source also removes its compiled file safely, and a compiled file you edited by hand is never deleted.

If you used the `tringify-theme` command from the theme tools, every one of its commands is available as `tringify theme <command>` with the same options, rules and output, and works on the same theme files.

`theme publish` only runs from a clean git work tree. The version's release notes record the commit it was built from, so every published version can be traced to its source. With `--install-store`, the new version is also installed, unpublished, into one of your organization's development stores.

## Signing in and access

`tringify login` opens the Tringify sign-in page in your browser. You choose the developer organization (or, with `--store`, the store) and approve the access the CLI asks for:

| Login | Access requested |
| --- | --- |
| Developer organization | View and edit your organization's themes and apps, release apps (submit, publish, private updates, install links), rotate app secrets, check whether an app can be retired or deleted, and list and create its development stores (`organization:themes:read`, `organization:themes:write`, `organization:apps:read`, `organization:apps:write`, `organization:apps:release`, `organization:apps:credentials`, `organization:apps:delete`, `organization:dev_stores:read`, `organization:dev_stores:write`) |
| Store | View and edit the store's storefront themes (`store:online_store.storefronts:read`, `store:online_store.storefronts:write`). With `theme dev --with-demo`, also add products and files (`store:products:write`, `store:files:write`). |

The CLI never receives more access than your own role in that organization or store, and if your role changes, the CLI's access changes with it. Each app action needs the matching permission in your organization: editing needs Edit Apps, releasing needs Release Apps, rotating a secret needs Rotate App Credentials, and `app retire` and `app delete` need Delete Apps. It cannot change your live theme, delete stores, retire or delete apps (you confirm those in the Developer Portal), create listings, change pricing, or read an app's existing client secret. If you signed in before app or development store access was added, run `tringify login` again to use `app` or `store` commands; the same applies to `app retire` and `app delete` if you signed in before they were added.

Sign-in uses OAuth with PKCE and a one-time listener on `127.0.0.1`, so your password never passes through the CLI. Access tokens last 15 minutes and are refreshed automatically; refresh tokens rotate on every use. Tokens are kept only in your system keychain: the macOS Keychain, Windows Credential Manager, or the Secret Service on Linux (GNOME Keyring, KWallet). Without one, sign-in stops and says so. If the keychain does not answer (for example a locked Secret Service over SSH, where it cannot show its unlock prompt), the CLI says it is waiting and stops after two minutes.

You can see and disconnect the CLI's access at any time in your Tringify account under **Connected tools**, or run `tringify logout`.

Working over SSH? Set `TRINGIFY_NO_BROWSER=1` and open the printed sign-in URL on your own machine; the browser must be able to reach `127.0.0.1` on the machine running the CLI (for example through SSH port forwarding).

## Use with AI agents

The same theme operations are available to MCP clients on the Tringify developer and store MCP endpoints, with the same access model. See the [Tringify developer documentation](https://dev-docs.tringify.com).

## Contributing

Bug reports and pull requests are welcome. Run `go test ./...` before sending a change.

## License

[MIT](LICENSE)
