# Security

## Reporting a vulnerability

Report it privately through GitHub: **Security → Report a vulnerability**
on this repository, or <https://github.com/belluikit/alidade/security/advisories/new>.
Please don't open a public issue for it. Include what you observed, how to
reproduce it, and the release (`alidade version` prints it) or commit.

## What alidade does with a repository

alidade reads a repository to build its graph, and some of that reading
runs code:

- **The Go layer** loads packages as `go build` would, through the `go`
  command on PATH, so the repository's `go.mod`, build constraints and cgo
  directives apply as they would to a build.
- **The Python layer** runs jedi in an embedded CPython over the
  repository's files, which it reads as source and does not import. It
  loads compiled extension modules only from the embedded interpreter's own
  library, never from the repository (jedi's `load_unsafe_extensions` is
  off).
- **The Svelte layer** runs svelte2tsx in an embedded V8 over each
  component's source. It translates the source; it does not run it.

So point alidade at a repository you would build yourself. It serves its
answers over stdio to the MCP client that started it and opens no network
port; the network it reaches is the `go` command's, which fetches modules
as a build would. Outside the repository it writes only:

- to the user's cache directory: the unpacked CPython and jedi, and jedi's
  answers kept between sessions;
- with `alidade setup`, Claude Code's settings (a backup beside them);
- with the grep hook (`alidade setup -enforce`), a log of refused calls
  at `~/.local/state/alidade/hook.log`.

## Bundled code

A release binary links V8 and CPython and embeds jedi and a Svelte
converter; `THIRD_PARTY_NOTICES.txt` lists everything it carries, at the
versions it carries. A vulnerability in one of them is fixed here by moving
to a fixed version and releasing. If one affects alidade, report it as
above, or open an issue asking for the update if it is already public.
